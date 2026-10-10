//go:build postgres

package e2e

import (
	"context"
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedConfigurationRejectsNullModuleWithoutMutation(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	_, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant
	var err error
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &management.Configuration{}})
	if err != nil {
		t.Fatal(err)
	}
	type snapshot struct {
		tenant    management.ConfigurationLayer
		fleet     management.FleetSettings
		authority management.AgentAuthority
		audits    int
	}
	read := func() snapshot {
		t.Helper()
		var s snapshot
		var err error
		if s.tenant, err = f.directory.TenantSettings(ctx, f.actor, f.tenant); err != nil {
			t.Fatal(err)
		}
		fleet, err := f.directory.FleetOverview(ctx, f.actor, f.tenant, f.actor)
		if err != nil {
			t.Fatal(err)
		}
		s.fleet = fleet.Settings
		if s.authority, err = f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE tenant_id=$1`, f.tenant).Scan(&s.audits); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := read()
	invalid := map[string]any{"modules": map[string]any{"files": nil}}
	for _, item := range []struct {
		path string
		body any
	}{
		{"/settings", map[string]any{"version": before.tenant.Version, "declaration": invalid}},
		{"/users/" + f.actor + "/fleet/settings", map[string]any{"version": before.fleet.Version, "configuration": invalid}},
		{"/agents/" + f.agent.ID, map[string]any{"version": f.agent.Version, "name": f.agent.Name, "configuration": invalid}},
	} {
		managementCall[any](t, f.client, "PUT", base+item.path, origin, item.body, 400)
		if after := read(); !reflect.DeepEqual(after, before) {
			t.Fatalf("invalid declaration changed durable state at %s", item.path)
		}
	}
	settings := before.tenant
	settings.Declaration = management.Configuration{Modules: map[agentpolicy.Capability]bool{agentpolicy.Files: false}}
	settings = managementCall[management.ConfigurationLayer](t, f.client, "PUT", base+"/settings", origin, settings, 200)
	afterOff := read()
	if afterOff.authority.Effective.Policy().Allows(agentpolicy.Files) || afterOff.authority.Agent.ExecutionEpoch != before.authority.Agent.ExecutionEpoch+1 {
		t.Fatal("explicit false did not revoke Files")
	}
	settings.Declaration = management.Configuration{}
	managementCall[management.ConfigurationLayer](t, f.client, "PUT", base+"/settings", origin, settings, 200)
	afterInherit := read()
	if !afterInherit.authority.Effective.Policy().Allows(agentpolicy.Files) || afterInherit.authority.Agent.ExecutionEpoch != afterOff.authority.Agent.ExecutionEpoch {
		t.Fatal("absent key did not inherit default without reviving old authority")
	}
}
