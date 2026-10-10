//go:build postgres

package e2e

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementConfigurationLayersPlansAndRevocation(t *testing.T) {
	_, d := managementDatabase(t)
	ctx := context.Background()
	owner, err := d.CreateUser(ctx, "layers@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Layers", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range []string{"primary", "second", "third"} {
		model, err := d.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: name, Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://provider.example.test/v1", APIKey: "private", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 4096, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, model.ID)
	}
	tenantSettings, err := d.TenantSettings(ctx, owner.ID, tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	tenantSettings.Declaration = management.Configuration{Models: ids[:2], Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: false}}
	tenantSettings, err = d.ConfigureTenantSettings(ctx, owner.ID, tenant.ID, tenantSettings)
	if err != nil {
		t.Fatal(err)
	}
	inherit, err := d.CreateAgent(ctx, owner.ID, tenant.ID, owner.ID, management.AgentConfig{Name: "inherits"})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := d.CreateAgent(ctx, owner.ID, tenant.ID, owner.ID, management.AgentConfig{Name: "independent", Configuration: &management.Configuration{Models: []string{ids[0], ids[2]}, Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: true}}})
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) management.AgentAuthority {
		t.Helper()
		v, e := d.AuthorizeAgent(ctx, owner.ID, tenant.ID, id)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	a, b := read(inherit.ID), read(explicit.ID)
	if !slices.Equal(a.Effective.Models, ids[:2]) || a.Effective.ModelSource.Layer != "tenant" || a.Effective.Policy().Allows(agentpolicy.MCP) {
		t.Fatal("Tenant inheritance", a)
	}
	pa, err := d.SnapshotPlan(ctx, modelCallScope(a))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := d.SnapshotPlan(ctx, modelCallScope(b))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Candidates[1].ModelID != ids[1] || pb.Candidates[1].ModelID != ids[2] {
		t.Fatal("independent model priorities were conflated", pa, pb)
	}
	overview, err := d.FleetOverview(ctx, owner.ID, tenant.ID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := overview.Settings
	settings.Configuration = management.Configuration{Models: []string{ids[2]}, Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: true}}
	settings, err = d.ConfigureFleet(ctx, owner.ID, tenant.ID, owner.ID, settings)
	if err != nil {
		t.Fatal(err)
	}
	beforeA, beforeB := read(inherit.ID), read(explicit.ID)
	if !beforeA.Effective.Policy().Allows(agentpolicy.MCP) || beforeA.Effective.ModelSource.Layer != "fleet" {
		t.Fatal(beforeA)
	}
	settings.Configuration.Modules[agentpolicy.MCP] = false
	settings, err = d.ConfigureFleet(ctx, owner.ID, tenant.ID, owner.ID, settings)
	if err != nil {
		t.Fatal(err)
	}
	a, b = read(inherit.ID), read(explicit.ID)
	if a.Agent.ExecutionEpoch != beforeA.Agent.ExecutionEpoch+1 || b.Agent.ExecutionEpoch != beforeB.Agent.ExecutionEpoch {
		t.Fatal("fanout revoked overridden Agent or missed inherited Agent", a, b)
	}
	settings.Configuration.Modules[agentpolicy.MCP] = true
	if _, err = d.ConfigureFleet(ctx, owner.ID, tenant.ID, owner.ID, settings); err != nil {
		t.Fatal(err)
	}
	if _, err = d.ResolveCandidate(ctx, modelCallScope(beforeA), pa.Candidates[0]); !errors.Is(err, management.ErrDenied) {
		t.Fatal("off/on revived old scope", err)
	}
	// Reordering changes future plans, while a persisted plan retains its route.
	if _, err = d.ResolveCandidate(ctx, modelCallScope(beforeB), pb.Candidates[1]); err != nil {
		t.Fatal("configuration-only edit invalidated frozen plan", err)
	}
	if err = d.SetTenantModels(ctx, tenant.ID, false, []string{ids[1]}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.SnapshotPlan(ctx, modelCallScope(read(explicit.ID))); !errors.Is(err, management.ErrModelUnavailable) {
		t.Fatal("fell back outside selected order", err)
	}
}

func TestManagementPresetChangesFenceInheritedTools(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	authority, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := f.directory.FleetOverview(ctx, f.actor, f.tenant, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	settings := fleet.Settings
	settings.Configuration.ModulePreset = "minimal"
	settings.Configuration.Modules = map[agentpolicy.Capability]bool{agentpolicy.Notes: true}
	settings, err = f.directory.ConfigureFleet(ctx, f.actor, f.tenant, f.actor, settings)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Agent.ExecutionEpoch <= authority.Agent.ExecutionEpoch || after.Effective.Policy().Allows(agentpolicy.Workers) || !after.Effective.Policy().Allows(agentpolicy.Notes) || !after.Effective.Policy().Allows(agentpolicy.Files) {
		t.Fatal("preset did not fence inherited tools/preserve explicit lower declarations", after)
	}
	settings.Configuration.ModulePreset = "standard"
	if _, err := f.directory.ConfigureFleet(ctx, f.actor, f.tenant, f.actor, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.SnapshotPlan(ctx, modelCallScope(authority)); !errors.Is(err, management.ErrDenied) {
		t.Fatal("preset off/on revived original scope", err)
	}
}
