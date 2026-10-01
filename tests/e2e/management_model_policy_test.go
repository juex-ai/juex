//go:build postgres

package e2e

import (
	"context"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementModelDefaultsAndTenantVisibility(t *testing.T) {
	_, d := managementDatabase(t)
	ctx := context.Background()
	user, err := d.CreateUser(ctx, "models@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Policy", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateTenant(ctx, "Inherits", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := management.ModelConfiguration{Provider: "fixture", Name: "one", Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://provider.example.test/v1", APIKey: "private", ContextWindow: 32768, MaxOutput: 4096, Enabled: true}
	one, err := d.ConfigureModel(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	config.Name = "two"
	two, err := d.ConfigureModel(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPlatformModel(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	agent, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Inherits"})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := d.AuthorizeAgent(ctx, user.ID, tenant.ID, agent.ID)
	if err != nil || authority.ModelID != one.ID {
		t.Fatal(authority, err)
	}
	overview, err := d.FleetOverview(ctx, user.ID, tenant.ID, user.ID)
	if err != nil || overview.Settings.DefaultModelID != "" || overview.PlatformDefaultModelID != one.ID {
		t.Fatal(overview, err)
	}
	settings := overview.Settings
	settings.DefaultModelID = two.ID
	if _, err := d.ConfigureFleet(ctx, user.ID, tenant.ID, user.ID, settings); err != nil {
		t.Fatal(err)
	}
	authority, err = d.AuthorizeAgent(ctx, user.ID, tenant.ID, agent.ID)
	if err != nil || authority.ModelID != two.ID {
		t.Fatal(authority, err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, false, []string{one.ID}); err != nil {
		t.Fatal(err)
	}
	models, err := d.Models(ctx, user.ID, tenant.ID)
	if err != nil || len(models) != 1 || models[0].ID != one.ID {
		t.Fatal(models, err)
	}
	if _, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Forbidden", ModelID: two.ID}); !errors.Is(err, management.ErrDenied) {
		t.Fatal("selected hidden model", err)
	}
	overview, err = d.FleetOverview(ctx, user.ID, tenant.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ConfigureFleet(ctx, user.ID, tenant.ID, user.ID, overview.Settings); !errors.Is(err, management.ErrDenied) {
		t.Fatal("fleet selected hidden model", err)
	}
	models, err = d.Models(ctx, user.ID, other.ID)
	if err != nil || len(models) != 2 {
		t.Fatal("policy leaked into another tenant", models, err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	models, err = d.Models(ctx, user.ID, tenant.ID)
	if err != nil || len(models) != 0 {
		t.Fatal("empty access list became inheritance", models, err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	models, err = d.Models(ctx, user.ID, tenant.ID)
	if err != nil || len(models) != 2 {
		t.Fatal(models, err)
	}
}

func modelCallScope(authority management.AgentAuthority) management.ModelCallScope {
	return management.ModelCallScope{ActorID: authority.ActorID, TenantID: authority.Fleet.TenantID, AgentID: authority.Agent.ID, UserID: authority.Fleet.UserID, FleetID: authority.Fleet.ID, ActorAuthorizationEpoch: authority.ActorAuthorizationEpoch, MembershipExecutionEpoch: authority.MembershipExecutionEpoch, AgentExecutionEpoch: authority.Agent.ExecutionEpoch}
}

func TestManagementModelPlanRevocationAndCredentialRouting(t *testing.T) {
	_, d := managementDatabase(t)
	ctx := context.Background()
	user, err := d.CreateUser(ctx, "plan@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Plan", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := management.ModelConfiguration{Provider: "fixture", Name: "primary", Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://provider.example.test/v1", APIKey: "first-key", ContextWindow: 32768, MaxOutput: 4096, Enabled: true}
	one, err := d.ConfigureModel(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	fallbackConfig := config
	fallbackConfig.Name = "fallback"
	two, err := d.ConfigureModel(ctx, fallbackConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPlatformModel(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetModelFallbacks(ctx, one.ID, []string{two.ID}); err != nil {
		t.Fatal(err)
	}
	// Fallback lists are flat, even when another model has its own list.
	if err := d.SetModelFallbacks(ctx, two.ID, []string{one.ID}); err != nil {
		t.Fatal(err)
	}
	agent, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Plan", Instructions: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := d.AuthorizeAgent(ctx, user.ID, tenant.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := modelCallScope(authority)
	plan, err := d.SnapshotPlan(ctx, scope)
	if err != nil || len(plan.Candidates) != 2 || plan.RequestedModelID != one.ID || plan.Instructions != "Original" || plan.Candidates[0].ModelID != one.ID || plan.Candidates[1].ModelID != two.ID {
		t.Fatal(plan, err)
	}
	resolve := func(candidate management.ModelCandidate, want string, wantErr error) {
		t.Helper()
		key, err := d.ResolveCandidate(ctx, scope, candidate)
		if !errors.Is(err, wantErr) || key != want {
			t.Fatalf("candidate admission failed: err=%v expected=%v; credential match=%v", err, wantErr, key == want)
		}
	}
	resolve(plan.Candidates[0], config.APIKey, nil)
	config.APIKey = "rotated-key"
	if _, err := d.ConfigureModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	resolve(plan.Candidates[0], config.APIKey, nil)
	config.Endpoint = "https://replacement.example.test/v1"
	if _, err := d.ConfigureModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	resolve(plan.Candidates[0], "", management.ErrModelUnavailable)
	config.Endpoint = plan.Candidates[0].Endpoint
	if _, err := d.ConfigureModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, false, []string{two.ID}); err != nil {
		t.Fatal(err)
	}
	resolve(plan.Candidates[0], "", management.ErrModelUnavailable)
	resolve(plan.Candidates[1], fallbackConfig.APIKey, nil)
	available, err := d.SnapshotPlan(ctx, scope)
	if err != nil || len(available.Candidates) != 1 || available.Candidates[0].ModelID != two.ID || available.RequestedModelID != one.ID {
		t.Fatal(available, err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	resolve(plan.Candidates[0], "", management.ErrModelUnavailable)
	resolve(plan.Candidates[1], fallbackConfig.APIKey, nil)
	fresh, err := d.SnapshotPlan(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	resolve(fresh.Candidates[0], config.APIKey, nil)
	// No-op policy and enabled writes must not revoke an unrelated candidate.
	if err := d.SetTenantModels(ctx, tenant.ID, false, []string{one.ID, two.ID}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetModelEnabled(ctx, one.ID, true); err != nil {
		t.Fatal(err)
	}
	resolve(fresh.Candidates[0], config.APIKey, nil)
	for _, configure := range []bool{false, true} {
		if configure {
			config.Enabled = false
			_, err = d.ConfigureModel(ctx, config)
		} else {
			err = d.SetModelEnabled(ctx, one.ID, false)
		}
		if err != nil {
			t.Fatal(err)
		}
		resolve(fresh.Candidates[0], "", management.ErrModelUnavailable)
		if configure {
			config.Enabled = true
			_, err = d.ConfigureModel(ctx, config)
		} else {
			err = d.SetModelEnabled(ctx, one.ID, true)
		}
		if err != nil {
			t.Fatal(err)
		}
		resolve(fresh.Candidates[0], "", management.ErrModelUnavailable)
		fresh, err = d.SnapshotPlan(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		resolve(fresh.Candidates[0], config.APIKey, nil)
	}
	for _, alter := range []func(*management.ModelCallScope){func(s *management.ModelCallScope) { s.UserID = tenant.ID }, func(s *management.ModelCallScope) { s.FleetID = tenant.ID }, func(s *management.ModelCallScope) { s.ActorAuthorizationEpoch++ }, func(s *management.ModelCallScope) { s.MembershipExecutionEpoch++ }, func(s *management.ModelCallScope) { s.AgentExecutionEpoch++ }} {
		foreign := scope
		alter(&foreign)
		if key, err := d.ResolveCandidate(ctx, foreign, fresh.Candidates[0]); !errors.Is(err, management.ErrDenied) || key != "" {
			t.Fatal("scope bypass", err)
		}
		if _, err := d.SnapshotPlan(ctx, foreign); !errors.Is(err, management.ErrDenied) {
			t.Fatal("snapshot scope bypass", err)
		}
	}
	for _, ids := range [][]string{{one.ID}, {two.ID, two.ID}, {two.ID, two.ID, two.ID, two.ID, two.ID}} {
		if err := d.SetModelFallbacks(ctx, one.ID, ids); !errors.Is(err, management.ErrInvalid) {
			t.Fatal("invalid list", err)
		}
	}
	if err := d.SetModelFallbacks(ctx, one.ID, nil); err != nil {
		t.Fatal(err)
	}
	fresh, err = d.SnapshotPlan(ctx, scope)
	if err != nil || len(fresh.Candidates) != 1 {
		t.Fatal(fresh, err)
	}
	// The previously admitted Turn retains its ordered candidate snapshot.
	resolve(plan.Candidates[1], fallbackConfig.APIKey, nil)
}
