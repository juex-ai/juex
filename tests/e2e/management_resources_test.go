//go:build postgres

package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementAgentOwnershipModelsAndLifecycle(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	admin, err := d.CreateUser(ctx, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	member, err := d.CreateUser(ctx, "member@example.test")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := d.CreateUser(ctx, "outside@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Agents", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.Invite(ctx, admin.ID, tenant.ID, member.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, member.ID, token); err != nil {
		t.Fatal(err)
	}
	config := management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: "http://localhost:12345/v1", APIKey: "a-private-provider-secret", ContextWindow: 32768, MaxOutput: 4096, Enabled: true}
	model, err := d.ConfigureModel(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted []byte
	if err := pool.QueryRow(ctx, `SELECT key_cipher FROM management.models WHERE id=$1`, model.ID).Scan(&encrypted); err != nil || string(encrypted) == config.APIKey {
		t.Fatal("provider key not encrypted", err)
	}
	overview, err := d.FleetOverview(ctx, member.ID, tenant.ID, member.ID)
	if err != nil || overview.Settings.Version != 1 {
		t.Fatal(overview, err)
	}
	settings := overview.Settings
	settings.DefaultModelID = model.ID
	settings, err = d.ConfigureFleet(ctx, member.ID, tenant.ID, member.ID, settings)
	if err != nil || settings.Version != 2 {
		t.Fatal(settings, err)
	}
	if _, err := d.ConfigureFleet(ctx, member.ID, tenant.ID, member.ID, overview.Settings); !errors.Is(err, management.ErrConflict) {
		t.Fatal("stale configuration accepted", err)
	}
	agent, err := d.CreateAgent(ctx, member.ID, tenant.ID, member.ID, management.AgentConfig{Name: "Research", Instructions: "Be precise."})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := d.AuthorizeAgent(ctx, member.ID, tenant.ID, agent.ID)
	if err != nil || authority.ModelID != model.ID || authority.Fleet.UserID != member.ID {
		t.Fatal(authority, err)
	}
	if _, err := d.FleetOverview(ctx, outside.ID, tenant.ID, member.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("cross-user access", err)
	}
	if _, err := d.CreateAgent(ctx, member.ID, tenant.ID, admin.ID, management.AgentConfig{Name: "Intruder"}); !errors.Is(err, management.ErrDenied) {
		t.Fatal("cross-user creation", err)
	}
	updated, err := d.ConfigureAgent(ctx, admin.ID, tenant.ID, agent.ID, agent.Version, management.AgentConfig{Name: "Assistant", ModelID: model.ID})
	if err != nil || updated.Version != 2 {
		t.Fatal(updated, err)
	}
	if _, err := d.ConfigureAgent(ctx, member.ID, tenant.ID, agent.ID, agent.Version, management.AgentConfig{Name: "Lost update"}); !errors.Is(err, management.ErrConflict) {
		t.Fatal(err)
	}
	archived, err := d.SetAgentArchived(ctx, admin.ID, tenant.ID, agent.ID, updated.Version, true)
	if err != nil || archived.Status != management.AgentArchived {
		t.Fatal(archived, err)
	}
	if _, err := d.AuthorizeAgent(ctx, admin.ID, tenant.ID, agent.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("archived agent can execute", err)
	}
	if _, err := d.SetAgentArchived(ctx, admin.ID, tenant.ID, agent.ID, archived.Version, false); err != nil {
		t.Fatal(err)
	}
	authority, err = d.AuthorizeAgent(ctx, member.ID, tenant.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	callScope := modelCallScope(authority)
	plan, err := d.SnapshotPlan(ctx, callScope)
	if err != nil {
		t.Fatal(err)
	}
	config.Enabled = false
	if _, err := d.ConfigureModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolveCandidate(ctx, callScope, plan.Candidates[0]); !errors.Is(err, management.ErrModelUnavailable) {
		t.Fatal("disabled model still callable", err)
	}
	if _, err := d.ChangeMember(ctx, admin.ID, tenant.ID, member.ID, management.Member, management.Suspended); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AuthorizeAgent(ctx, admin.ID, tenant.ID, agent.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("admin bypassed suspended execution", err)
	}
	if _, err := d.ConfigureAgent(ctx, admin.ID, tenant.ID, agent.ID, 4, management.AgentConfig{Name: "forbidden"}); !errors.Is(err, management.ErrDenied) {
		t.Fatal(err)
	}
	retained, err := d.FleetOverview(ctx, admin.ID, tenant.ID, member.ID)
	if err != nil || len(retained.Agents) != 1 || retained.Owner.ID != member.ID {
		t.Fatal(retained, err)
	}
	var audit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE actor_id=$1 AND owner_id=$2 AND agent_id=$3`, admin.ID, member.ID, agent.ID).Scan(&audit); err != nil || audit < 3 {
		t.Fatal("delegated changes have no audit", audit, err)
	}
}
