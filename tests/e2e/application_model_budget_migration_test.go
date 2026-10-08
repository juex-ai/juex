//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"testing"
	"time"
)

func TestApplicationModelBudgetMigrationKeepsReceiptsAndAttempts(t *testing.T) {
	for _, state := range []string{"queued", "active", "waiting", "completed", "cancelled", "tombstone"} {
		t.Run(state, func(t *testing.T) {
			pool, _, scope, _ := runtimeDatabase(t)
			installPreOutputBudgetSchema(t, pool, "runtime", []string{"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "instructions_schema.sql", "output_budget_schema.sql", "main_triggers_schema.sql"})
			ctx := context.Background()
			store := runtimepg.New(pool)
			if _, err := store.EnsureAgent(ctx, scope); err != nil {
				t.Fatal(err)
			}
			job := applicationJob()
			if state == "tombstone" {
				if err := store.CancelApplication(ctx, scope, job.Application, job.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				receipt, err := store.AdmitApplication(ctx, scope, job)
				if err != nil {
					t.Fatal(err)
				}
				if state != "queued" {
					lease, err := store.Claim(ctx, scope.AgentID, "old-runtime", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					config := runtimeConfig()
					config.Models[0].MaxOutput = 0
					work, err := store.BeginTurn(ctx, lease, scope, receipt.InputID, config)
					if err != nil {
						t.Fatal(err)
					}
					attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Model: config.Models[0], Purpose: "application:memory"})
					if err != nil {
						t.Fatal(err)
					}
					switch state {
					case "completed":
						err = store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Reviewed"), StopReason: llm.StopEndTurn}, "")
					case "waiting":
						err = store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "search", ToolName: "memory_search", Input: map[string]any{"query": "evidence"}}}}, StopReason: llm.StopToolUse}, "")
					case "cancelled":
						err = store.CancelApplication(ctx, scope, job.Application, job.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := store.ApplicationReceipt(ctx, scope, job.Application, job.ID)
			if err != nil {
				t.Fatal(err)
			}
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
