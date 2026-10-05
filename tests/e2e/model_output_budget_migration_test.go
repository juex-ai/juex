//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

// These pools belong to managementDatabase's disposable test databases. Install
// the published DDL and checksums, rather than guessing an old database shape.
func installPreOutputBudgetSchema(t *testing.T, pool *pgxpool.Pool, owner string, files []string) {
	t.Helper()
	ctx := context.Background()
	schema := pgx.Identifier{owner}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; CREATE SCHEMA "+schema+"; CREATE TABLE "+schema+".schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	module := owner
	if owner == "runtime" {
		module = "managedruntime"
	}
	for i, name := range files {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../internal", module, "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(name, err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO "+schema+".schema_versions VALUES($1,$2)", i+1, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagementOutputBudgetMigrationPreservesPositivePolicy(t *testing.T) {
	pool, directory := managementDatabase(t)
	installPreOutputBudgetSchema(t, pool, "management", []string{"schema.sql", "auth_schema.sql", "mail_schema.sql", "resources_schema.sql", "authority_schema.sql", "model_policy_schema.sql", "workers_schema.sql", "applications_schema.sql", "notifications_schema.sql", "purge_schema.sql", "retention_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "managed_environment_receipts_schema.sql", "capabilities_schema.sql", "instructions_schema.sql"})
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO management.models(provider,name,protocol,endpoint,key_cipher,context_window,max_output,authorization_epoch) VALUES('fixture','existing','openai/chat','https://provider.example.test/v1','retained-cipher',32768,2048,7) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := managementpg.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var cap, reserve, epoch int
	var cipher []byte
	if err := pool.QueryRow(ctx, `SELECT max_output,output_reserve,authorization_epoch,key_cipher FROM management.models WHERE id=$1`, id).Scan(&cap, &reserve, &epoch, &cipher); err != nil || cap != 2048 || reserve != 2048 || epoch != 7 || string(cipher) != "retained-cipher" {
		t.Fatal(cap, reserve, epoch, err)
	}
	user, err := directory.CreateUser(ctx, "upgrade@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Upgrade", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	models, err := directory.Models(ctx, user.ID, tenant.ID)
	if err != nil || len(models) != 1 || models[0].ID != id || models[0].OutputReserve != 2048 {
		t.Fatal(models, err)
	}
	_, err = directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "existing", Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://provider.example.test/v1", APIKey: "new-credential", ContextWindow: 32768, MaxOutput: 2048, OutputReserve: 2048, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT authorization_epoch FROM management.models WHERE id=$1`, id).Scan(&epoch); err != nil || epoch != 7 {
		t.Fatal("migration caused an unchanged policy to revoke old plans", epoch, err)
	}
}

func TestManagedOutputBudgetMigrationPreservesRecoveryAndHistory(t *testing.T) {
	for _, state := range []string{"running", "waiting", "completed"} {
		t.Run(state, func(t *testing.T) {
			pool, _, scope, _ := runtimeDatabase(t)
			installPreOutputBudgetSchema(t, pool, "runtime", []string{"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "instructions_schema.sql"})
			ctx := context.Background()
			store := runtimepg.New(pool)
			// Seed the published schema directly. Current store reads may require
			// columns introduced by later migrations and cannot create old state.
			if _, err := pool.Exec(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4)`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID); err != nil {
				t.Fatal(err)
			}
			var threadID string
			if err := pool.QueryRow(ctx, `INSERT INTO runtime.threads(agent_id,kind,name,state) VALUES($1,'main','Main','running') RETURNING id`, scope.AgentID).Scan(&threadID); err != nil {
				t.Fatal(err)
			}
			input := managedruntime.InputReceipt{ThreadID: threadID}
			inputState := "active"
			if state == "completed" {
				inputState = "completed"
			}
			if err := pool.QueryRow(ctx, `INSERT INTO runtime.inputs(request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state) VALUES('upgrade',$1,$2,$3,$4,$5,$6,'Keep this work',$7) RETURNING id`, threadID, scope.ActorID, scope.ActorAuthorizationEpoch, scope.MembershipVersion, scope.MembershipExecutionEpoch, scope.AgentExecutionEpoch, inputState).Scan(&input.ID); err != nil {
				t.Fatal(err)
			}
			lease, err := store.Claim(ctx, scope.AgentID, "before-upgrade", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			config := runtimeConfig()
			backup := config.Models[0]
			backup.ModelID = "00000000-0000-4000-8000-000000000002"
			backup.Model, backup.MaxOutput, backup.OutputReserve = "backup", 2048, 2048
			config.Models = append(config.Models, backup)
			encoded, _ := json.Marshal(config)
			work := managedruntime.Work{ThreadID: threadID, InputID: input.ID, Generation: 1}
			if err := pool.QueryRow(ctx, `INSERT INTO runtime.turns(input_id,thread_id,generation,config,activation_epoch,state,model_index) VALUES($1,$2,1,$3,$4,$5,1) RETURNING id`, input.ID, threadID, encoded, lease.Epoch, state).Scan(&work.TurnID); err != nil {
				t.Fatal(err)
			}
			attempt := managedruntime.Attempt{TurnID: work.TurnID}
			request, _ := json.Marshal(managedruntime.ModelRequest{Model: backup, MaxOutputTokens: 2048, Purpose: "conversation", Generation: 1})
			attemptState := "started"
			if state == "completed" {
				attemptState = "completed"
			}
			if err := pool.QueryRow(ctx, `INSERT INTO runtime.attempts(turn_id,ordinal,request,state) VALUES($1,1,$2,$3) RETURNING id`, work.TurnID, request, attemptState).Scan(&attempt.ID); err != nil {
				t.Fatal(err)
			}
			// Build the exact pre-upgrade JSON contract, which contains no reserve.
			if _, err := pool.Exec(ctx, `UPDATE runtime.turns SET state=$2,config=config #- '{models,0,output_reserve}' #- '{models,1,output_reserve}' WHERE id=$1`, work.TurnID, state); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE runtime.attempts SET request=request #- '{model,output_reserve}' WHERE id=$1`, attempt.ID); err != nil {
				t.Fatal(err)
			}
			var oldRequest, oldPlan []byte
			if err := pool.QueryRow(ctx, `SELECT request FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&oldRequest); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT config FROM runtime.turns WHERE id=$1`, work.TurnID).Scan(&oldPlan); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := runtimepg.Migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
			}
			var savedRequest, savedPlan []byte
			var index int
			if err := pool.QueryRow(ctx, `SELECT request FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&savedRequest); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT config,model_index FROM runtime.turns WHERE id=$1`, work.TurnID).Scan(&savedPlan, &index); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(oldRequest, savedRequest) || index != 1 {
				t.Fatal("migration rewrote historical request or fallback position")
			}
			if state == "completed" {
				if !bytes.Equal(oldPlan, savedPlan) {
					t.Fatal("completed plan was rewritten")
				}
				return
			}
			var upgraded managedruntime.TurnConfig
			if err := json.Unmarshal(savedPlan, &upgraded); err != nil || len(upgraded.Models) != 2 || upgraded.Models[0].OutputReserve != 4096 || upgraded.Models[1].OutputReserve != 2048 {
				t.Fatal(upgraded, err)
			}
			if err := store.Release(ctx, lease); err != nil {
				t.Fatal(err)
			}
			restarted := runtimepg.New(pool)
			lease, err = restarted.Claim(ctx, scope.AgentID, "after-upgrade", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := restarted.BeginTurn(ctx, lease, scope, input.ID, managedruntime.TurnConfig{})
			if err != nil || recovered.ModelIndex != 1 || recovered.Config.Models[1] != backup {
				t.Fatal("old turn did not resume its frozen budget", recovered.Config, err)
			}
			if _, err := restarted.BeginAttempt(ctx, lease, recovered.TurnID, managedruntime.ModelRequest{Model: backup, MaxOutputTokens: 2048, Purpose: "conversation"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
