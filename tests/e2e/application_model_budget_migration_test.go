//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"testing"
)

func TestApplicationModelBudgetMigrationKeepsReceiptsAndAttempts(t *testing.T) {
	for _, state := range []string{"queued", "active", "waiting", "completed", "cancelled", "tombstone"} {
		t.Run(state, func(t *testing.T) {
			pool, _, scope, _ := runtimeDatabase(t)
			installPreOutputBudgetSchema(t, pool, "runtime", []string{"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "instructions_schema.sql", "output_budget_schema.sql", "main_triggers_schema.sql"})
			ctx := context.Background()
			store := runtimepg.New(pool)
			job := applicationJob()
			before := seedPreApplicationBudgetJob(t, pool, scope, job, state)
			var historyBefore []byte
			if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]'::jsonb) FROM runtime.attempts a`).Scan(&historyBefore); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := runtimepg.Migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
			}
			var historyAfter, encoded []byte
			if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]'::jsonb) FROM runtime.attempts a`).Scan(&historyAfter); err != nil || !bytes.Equal(historyBefore, historyAfter) {
				t.Fatal("migration changed attempt history", err)
			}
			if err := pool.QueryRow(ctx, `SELECT request FROM runtime.application_jobs WHERE job_id=$1`, job.ID).Scan(&encoded); err != nil {
				t.Fatal(err)
			}
			if state == "tombstone" {
				if encoded != nil {
					t.Fatal("cancel-first acquired a request")
				}
			} else {
				var stored managedruntime.ApplicationJob
				if err := json.Unmarshal(encoded, &stored); err != nil || stored.ModelBudget == nil || *stored.ModelBudget != *memoryModelBudget() || stored.MaxCalls != job.MaxCalls {
					t.Fatal(stored, err)
				}
			}
			job.ModelBudget = memoryModelBudget()
			after, err := store.AdmitApplication(ctx, scope, job)
			if err != nil || after.ThreadID != before.ThreadID || after.InputID != before.InputID || after.State != before.State {
				t.Fatal("upgrade replay changed state/identity", before, after, err)
			}
			changed := job
			changed.ModelBudget = &managedruntime.ApplicationModelBudget{ContextWindow: 16384, MaxOutput: 2048}
			if _, err := store.AdmitApplication(ctx, scope, changed); !errors.Is(err, managedruntime.ErrConflict) {
				t.Fatal("upgraded retry changed frozen policy", err)
			}
		})
	}
}

// Seed published DDL directly: new Store methods may require columns that do
// not exist before the upgrade under test.
func seedPreApplicationBudgetJob(t *testing.T, pool *pgxpool.Pool, scope managedruntime.Scope, job managedruntime.ApplicationJob, state string) managedruntime.ApplicationReceipt {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4)`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID); err != nil {
		t.Fatal(err)
	}
	var main string
	if err := pool.QueryRow(ctx, `INSERT INTO runtime.threads(agent_id,kind,name) VALUES($1,'main','Main') RETURNING id`, scope.AgentID).Scan(&main); err != nil {
		t.Fatal(err)
	}
	encodedScope, _ := json.Marshal(scope)
	before := managedruntime.ApplicationReceipt{Application: job.Application, ID: job.ID, State: state}
	if state == "tombstone" {
		if _, err := pool.Exec(ctx, `INSERT INTO runtime.application_jobs(application,fleet_id,job_id,agent_id,scope,cancelled) VALUES($1,$2,$3,$4,$5,true)`, job.Application, scope.FleetID, job.ID, scope.AgentID, encodedScope); err != nil {
			t.Fatal(err)
		}
		before.State = "cancelled"
		return before
	}
	inputState, threadState, turnState, attemptState := state, "idle", state, "completed"
	switch state {
	case "queued":
		threadState = "queued"
	case "active":
		threadState, turnState, attemptState = "running", "running", "started"
	case "waiting":
		inputState, threadState = "active", "waiting"
	case "cancelled":
		attemptState = "unknown"
	}
	before.State = inputState
	if err := pool.QueryRow(ctx, `INSERT INTO runtime.threads(agent_id,parent_id,kind,name,state) VALUES($1,$2,'worker',$3,$4) RETURNING id`, scope.AgentID, main, job.Name, threadState).Scan(&before.ThreadID); err != nil {
		t.Fatal(err)
	}
	source, _ := json.Marshal(managedruntime.InputSource{Kind: "application", Application: job.Application, ApplicationJobID: job.ID})
	if err := pool.QueryRow(ctx, `INSERT INTO runtime.inputs(request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state,source) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, "app/"+job.Application+"/"+job.ID, before.ThreadID, scope.ActorID, scope.ActorAuthorizationEpoch, scope.MembershipVersion, scope.MembershipExecutionEpoch, scope.AgentExecutionEpoch, job.Instruction, inputState, source).Scan(&before.InputID); err != nil {
		t.Fatal(err)
	}
	encodedJob, _ := json.Marshal(job)
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.application_jobs(application,fleet_id,job_id,agent_id,scope,request,thread_id,input_id,cancelled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, job.Application, scope.FleetID, job.ID, scope.AgentID, encodedScope, encodedJob, before.ThreadID, before.InputID, state == "cancelled"); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		config := runtimeConfig()
		config.Models[0].MaxOutput = 0
		encodedConfig, _ := json.Marshal(config)
		var turn string
		if err := pool.QueryRow(ctx, `INSERT INTO runtime.turns(input_id,thread_id,generation,config,activation_epoch,state) VALUES($1,$2,1,$3,1,$4) RETURNING id`, before.InputID, before.ThreadID, encodedConfig, turnState).Scan(&turn); err != nil {
			t.Fatal(err)
		}
		request, _ := json.Marshal(managedruntime.ModelRequest{Model: config.Models[0], Purpose: "application:memory"})
		var response []byte
		switch state {
		case "completed":
			response, _ = json.Marshal(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Reviewed"), StopReason: llm.StopEndTurn})
		case "waiting":
			response, _ = json.Marshal(llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "search", ToolName: "memory_search", Input: map[string]any{"query": "evidence"}}}}, StopReason: llm.StopToolUse})
		}
		if _, err := pool.Exec(ctx, `INSERT INTO runtime.attempts(turn_id,ordinal,request,response,state) VALUES($1,1,$2,$3,$4)`, turn, request, response, attemptState); err != nil {
			t.Fatal(err)
		}
	}
	return before
}
