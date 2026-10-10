//go:build postgres

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedInputTrackingUpgradeFreezesExistingTurns(t *testing.T) {
	pool, _ := managementDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA runtime;CREATE TABLE runtime.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	previous := []string{"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "instructions_schema.sql", "output_budget_schema.sql", "main_triggers_schema.sql", "application_budget_schema.sql", "import_schema.sql", "thread_application_schema.sql", "thread_state_schema.sql", "input_images_schema.sql", "progress_schema.sql", "observer_management_schema.sql", "thread_deletion_schema.sql"}
	for i, name := range previous {
		data, err := os.ReadFile(filepath.Join("../../internal/managedruntime/postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(name, err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO runtime.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	agent := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4)`, agent, uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(managedruntime.ModelRequest{System: "original frozen request", Model: runtimeConfig().Models[0], MaxOutputTokens: 4096, Purpose: "conversation", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	type record struct {
		turn, attempt string
		before        string
		plan          string
		want          managedruntime.TurnConfig
		recoverable   bool
	}
	var records []record
	for _, state := range []string{"running", "waiting", "completed", "cancelled", "failed"} {
		for _, policy := range []agentpolicy.Policy{
			{Disabled: []agentpolicy.Capability{agentpolicy.Notes}, Enabled: []agentpolicy.Capability{agentpolicy.ApplyPatch}},
			{Version: 1, SkillSources: true, Disabled: []agentpolicy.Capability{agentpolicy.Notes, agentpolicy.FileSearch}, Enabled: []agentpolicy.Capability{agentpolicy.ApplyPatch}},
		} {
			thread, input, turn, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			config := runtimeConfig()
			config.Capabilities = policy
			queries := []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO runtime.threads(id,agent_id,kind,name) VALUES($1,$2,'worker','Old')`, []any{thread, agent}},
				{`INSERT INTO runtime.inputs(id,request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state) VALUES($1::uuid,$1::text,$2,$3,1,1,1,1,'old','completed')`, []any{input, thread, uuid.NewString()}},
				{`INSERT INTO runtime.turns(id,input_id,thread_id,generation,config,activation_epoch,state) VALUES($1,$2,$3,1,$4,1,$5)`, []any{turn, input, thread, config, state}},
				{`INSERT INTO runtime.attempts(id,turn_id,ordinal,request,state) VALUES($1,$2,0,$3,'completed')`, []any{attempt, turn, request}},
			}
			for _, q := range queries {
				if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
					t.Fatal(err)
				}
			}
			var before, plan string
			if err := pool.QueryRow(ctx, `SELECT request::text FROM runtime.attempts WHERE id=$1`, attempt).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT config::text FROM runtime.turns WHERE id=$1`, turn).Scan(&plan); err != nil {
				t.Fatal(err)
			}
			recoverable := state == "running" || state == "waiting"
			if recoverable {
				config.Capabilities.Disabled = append(config.Capabilities.Disabled, agentpolicy.InputTracking)
			}
			records = append(records, record{turn, attempt, before, plan, config, recoverable})
		}
	}
	if err := runtimepg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := runtimepg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		var config managedruntime.TurnConfig
		var after, plan string
		if err := pool.QueryRow(ctx, `SELECT t.config,t.config::text,a.request::text FROM runtime.turns t JOIN runtime.attempts a ON a.turn_id=t.id WHERE t.id=$1 AND a.id=$2`, record.turn, record.attempt).Scan(&config, &plan, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(config, record.want) || record.before != after {
			t.Fatal("upgrade changed frozen authority, model plan or dispatched request", config.Capabilities, record.want.Capabilities)
		}
		if !record.recoverable && plan != record.plan {
			t.Fatal("upgrade rewrote terminal plan")
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.input_tracking`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("upgrade fabricated historical checks", rows, err)
	}
	if !runtimeConfig().Capabilities.Allows(agentpolicy.InputTracking) {
		t.Fatal("new turn default disabled")
	}
}
