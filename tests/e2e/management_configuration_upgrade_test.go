//go:build postgres

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/importproof"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagementConfigurationUpgradeRetainsSelectionAndImportProof(t *testing.T) {
	pool := emptyManagementDatabase(t)
	ctx := context.Background()
	// Install the real prior schema with its original checksums, then insert
	// retained state before invoking the current transactional migration.
	files := []string{"schema.sql", "auth_schema.sql", "mail_schema.sql", "resources_schema.sql", "authority_schema.sql", "model_policy_schema.sql", "workers_schema.sql", "applications_schema.sql", "notifications_schema.sql", "purge_schema.sql", "retention_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "managed_environment_receipts_schema.sql", "capabilities_schema.sql", "instructions_schema.sql", "output_budget_schema.sql", "model_options_schema.sql", "import_schema.sql", "model_import_schema.sql"}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA management; CREATE TABLE management.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i, file := range files {
		data, err := os.ReadFile(filepath.Join("../../internal/management/postgres", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(file, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO management.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	var agents importproof.AgentsV1
	var models importproof.ModelsV1
	for name, dest := range map[string]any{"agent-full.json": &agents, "models.json": &models} {
		data, err := os.ReadFile(filepath.Join("../../internal/management/importproof/testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, dest); err != nil {
			t.Fatal(err)
		}
	}
	actor, fleet, tenant, agentID, inheritID := uuid.NewString(), agents.ExpectedFleetID, models.TenantID, uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO management.users(id,email) VALUES($1,'upgrade@example.test')`, actor)
	exec(`INSERT INTO management.tenants(id,name) VALUES($1,'Upgrade')`, tenant)
	exec(`INSERT INTO management.memberships(tenant_id,user_id,role,status) VALUES($1,$2,'admin','active')`, tenant, actor)
	exec(`INSERT INTO management.fleets(id,tenant_id,user_id) VALUES($1,$2,$3)`, fleet, tenant, actor)
	box, err := secrets.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[management.ModelKey]management.ModelImportIdentity{}
	type modelReceipt struct {
		Key management.ModelKey `json:"key"`
		management.ModelImportIdentity
	}
	var receipt []modelReceipt
	for i, item := range models.Models {
		c := item.Configuration
		id := uuid.NewString()
		if i == 0 {
			id = agents.Agents[0].Config.ModelID
		}
		key := management.ModelKey{Provider: c.Provider, Name: c.Name}
		identity := management.ModelImportIdentity{ID: id, ModelAuthorizationEpoch: 1}
		ids[key] = identity
		receipt = append(receipt, modelReceipt{key, identity})
		keyCipher, err := box.Seal("model:"+id, []byte(c.APIKey))
		if err != nil {
			t.Fatal(err)
		}
		options, _ := json.Marshal(c.Options)
		optionsCipher, err := box.Seal("model-options:"+id, options)
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO management.models(id,provider,name,protocol,endpoint,key_cipher,context_window,max_output,output_reserve,enabled,options_cipher) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, c.Provider, c.Name, c.Protocol, c.Endpoint, keyCipher, c.ContextWindow, c.MaxOutput, c.OutputReserve, c.Enabled, optionsCipher)
	}
	for _, item := range models.Models {
		for i, key := range item.Fallbacks {
			exec(`INSERT INTO management.model_fallbacks VALUES($1,$2,$3)`, ids[management.ModelKey{Provider: item.Configuration.Provider, Name: item.Configuration.Name}].ID, ids[key].ID, i+1)
		}
	}
	primary := agents.Agents[0].Config.ModelID
	secondary := ids[management.ModelKey{Provider: "fixture", Name: "b"}].ID
	exec(`UPDATE management.platform_settings SET default_model_id=$1`, secondary)
	exec(`INSERT INTO management.fleet_settings(fleet_id,default_model_id) VALUES($1,$2)`, fleet, primary)
	c := agents.Agents[0].Config
	exec(`INSERT INTO management.agents(id,fleet_id,name,instructions,model_id,capabilities,worker_depth,dynamic_instructions,hooks) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, agentID, fleet, c.Name, c.Instructions, c.ModelID, c.Capabilities, c.WorkerDepth, c.DynamicInstructions, c.Hooks)
	exec(`INSERT INTO management.agents(id,fleet_id,name) VALUES($1,$2,'Inherited')`, inheritID, fleet)
	mapping := map[string]string{agents.Agents[0].SourceAgentID: agentID}
	exec(`INSERT INTO management.agent_imports(fleet_id,source,source_sha256,payload_sha256,agents) VALUES($1,$2,$3,$4,$5)`, fleet, agents.Source, agents.SourceSHA256, "a93421e23f39f4e0ff624c2df2e404fdb9a13696509717ef3eebdd14e3296c81", mapping)
	exec(`INSERT INTO management.model_imports(tenant_id,source,source_sha256,payload_sha256,models) VALUES($1,$2,$3,$4,$5)`, tenant, models.Source, models.SourceSHA256, "90087ef237621ea4d2cf2f6afb89920006c65875c5ac48d5de921b5cb18a5c54", receipt)
	if err := managementpg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := managementpg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	d := managementpg.NewDirectory(pool, managementpg.Config{Secrets: box, PublicURL: "http://localhost:8680"})
	for _, id := range []string{agentID, inheritID} {
		authority, err := d.AuthorizeAgent(ctx, actor, tenant, id)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{primary, secondary, ids[management.ModelKey{Provider: "fixture", Name: "c"}].ID}
		if !slices.Equal(authority.Effective.Models, want) || len(authority.Agent.Configuration.Modules) != 14 || authority.Agent.ExecutionEpoch != 1 {
			t.Fatal("upgrade changed selection or authority", authority)
		}
		if id == agentID && authority.Effective.Policy().Allows(agentpolicy.MCP) {
			t.Fatal("upgrade enabled disabled module")
		}
		if id == inheritID && (authority.Effective.ModelSource.Layer != "fleet" || len(authority.Agent.Configuration.Models) != 0) {
			t.Fatal("upgrade lost inheritance")
		}
	}
	settings, err := d.TenantSettings(ctx, actor, tenant)
	if err != nil || !slices.Equal(settings.Declaration.Models, []string{secondary}) {
		t.Fatal("deployment default not preserved for Tenant", err)
	}
	currentModels, _, err := models.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	currentAgents, _, err := agents.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ImportModels(ctx, currentModels); !errors.Is(err, management.ErrImportProofVersion) {
		t.Fatal("normal import downgraded proof", err)
	}
	if _, err := d.ImportAgents(ctx, actor, tenant, actor, currentAgents); !errors.Is(err, management.ErrImportProofVersion) {
		t.Fatal("normal Agent import downgraded proof", err)
	}
	edited, err := d.ConfigureAgent(ctx, actor, tenant, agentID, 1, management.AgentConfig{Name: "Later name", Instructions: "Later instructions", Configuration: &management.Configuration{Models: []string{secondary}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAgentArchived(ctx, actor, tenant, agentID, edited.Version, true); err != nil {
		t.Fatal(err)
	}
	rotated := currentModels.Models[0].Configuration
	rotated.APIKey = "later-private-key"
	if _, err := managed.ConfigureModel(ctx, d, rotated); err != nil {
		t.Fatal(err)
	}
	if err := d.SetModelEnabled(ctx, primary, false); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTenantModels(ctx, tenant, false, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM management.agents x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM management.models x),(SELECT jsonb_agg(to_jsonb(x)) FROM management.agent_imports x),(SELECT jsonb_agg(to_jsonb(x)) FROM management.model_imports x),(SELECT count(*) FROM management.audit),(SELECT count(*) FROM management.operator_audit))::text`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	againModels, err := d.RecoverModelImportV1(ctx, models)
	if err != nil || !reflect.DeepEqual(againModels, ids) {
		t.Fatal("old model receipt did not recover original identities", err)
	}
	againAgents, err := d.RecoverAgentImportV1(ctx, actor, tenant, actor, agents)
	if err != nil || !reflect.DeepEqual(againAgents, mapping) {
		t.Fatal("old Agent receipt did not recover original identities", err)
	}
	if snapshot() != before {
		t.Fatal("recovery modified later state, proof or audit")
	}
	for _, mutate := range []func(*importproof.ModelsV1){func(v *importproof.ModelsV1) { slices.Reverse(v.Models[0].Fallbacks) }, func(v *importproof.ModelsV1) { v.Models[0].Configuration.APIKey += "changed" }} {
		raw, _ := json.Marshal(models)
		var changed importproof.ModelsV1
		_ = json.Unmarshal(raw, &changed)
		mutate(&changed)
		if _, err := d.RecoverModelImportV1(ctx, changed); !errors.Is(err, management.ErrConflict) {
			t.Fatal("changed proof accepted", err)
		}
	}
	agents.Agents[0].Config.Instructions += "changed"
	if _, err := d.RecoverAgentImportV1(ctx, actor, tenant, actor, agents); !errors.Is(err, management.ErrConflict) {
		t.Fatal("changed Agent proof accepted", err)
	}
	if snapshot() != before {
		t.Fatal("rejected proof changed state")
	}
}
