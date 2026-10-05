//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedRuntimeApplicationObservationPreservesLiveCalendarOnly(t *testing.T) {
	for _, name := range []string{"live-calendar", "historical-memory", "historical-calendar", "imported-calendar-job"} {
		t.Run(name, func(t *testing.T) {
			historical := name != "live-calendar"
			pool, store, scope, _ := runtimeDatabase(t)
			ctx := context.Background()
			scope.AgentID = uuid.NewString()
			data := runtimeImportFixture(t, scope.AgentID)
			data.Threads[1].Application = &managedruntime.ImportedApplication{Application: "memory"}
			data.Threads[1].Thread.Retention = "active"
			if name == "historical-calendar" || name == "imported-calendar-job" {
				data.Threads[1].Thread.Application = "calendar"
				data.Threads[1].Application.Application = "calendar"
			}
			if name == "imported-calendar-job" {
				data.Threads[1].Application.JobID, data.Threads[1].Application.State = "historical-job", "completed"
			}
			if err := store.ImportAgent(ctx, scope, data); err != nil {
				t.Fatal(err)
			}
			job := applicationJob()
			job.Application, job.MaxCalls = "calendar", 3
			receipt, err := store.AdmitApplication(ctx, scope, job)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := store.Claim(ctx, scope.AgentID, "subscription", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			work, err := store.BeginTurn(ctx, lease, scope, receipt.InputID, runtimeConfig())
			if err != nil {
				t.Fatal(err)
			}
			request := managedruntime.ModelRequest{Messages: work.History}
			attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
			if err != nil {
				t.Fatal(err)
			}
			call := llm.Block{Type: llm.BlockToolUse, ToolUseID: "subscribe", ToolName: "subscribe", Input: map[string]any{"kind": "environment.presence", "environment_id": "device"}}
			if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{call}}}, ""); err != nil {
				t.Fatal(err)
			}
			tool, err := store.ClaimTool(ctx, "subscription")
			if err != nil {
				t.Fatal(err)
			}
			sub, err := store.ApplySubscription(ctx, tool, managedruntime.SubscriptionRequest{Kind: "environment.presence", EnvironmentID: "device"}, 1, 0, "", json.RawMessage(`{"online":false}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishTool(ctx, tool, managedruntime.ToolOutcome{State: "ready", Content: "subscribed"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginTurn(ctx, lease, scope, receipt.InputID, runtimeConfig()); err != nil {
				t.Fatal(err)
			}
			attempt, err = store.BeginAttempt(ctx, lease, work.TurnID, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Waiting for notification"), StopReason: llm.StopEndTurn}, ""); err != nil {
				t.Fatal(err)
			}
			target := receipt.ThreadID
			if historical {
				// A delayed persisted delivery must not reactivate retained history.
				target = data.Threads[1].Thread.ID
				if _, err := pool.Exec(ctx, `UPDATE runtime.subscriptions SET thread_id=$2 WHERE id=$1`, sub.ID, target); err != nil {
					t.Fatal(err)
				}
			}
			delivery, err := store.ClaimObservationDelivery(ctx, "notification")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishObservationDelivery(ctx, delivery, true, json.RawMessage(`{"online":true}`)); err != nil {
				t.Fatal(err)
			}
			var state string
			var enabled bool
			if err := pool.QueryRow(ctx, `SELECT d.state,s.enabled FROM runtime.observation_deliveries d JOIN runtime.subscriptions s ON s.id=d.subscription_id WHERE d.id=$1`, delivery.ID).Scan(&state, &enabled); err != nil {
				t.Fatal(err)
			}
			if historical {
				var inputs int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE thread_id=$1`, target).Scan(&inputs); err != nil || inputs != 0 || state != "skipped" || enabled {
					t.Fatal("historical purpose admitted or retried an observation", inputs, state, enabled, err)
				}
				encodedScope, _ := json.Marshal(scope)
				var threadSub string
				if err := pool.QueryRow(ctx, `INSERT INTO runtime.thread_subscriptions(agent_id,thread_id,worker_id,scope) VALUES($1,$2,$3,$4) RETURNING id`, scope.AgentID, target, receipt.ThreadID, encodedScope).Scan(&threadSub); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO runtime.thread_deliveries(subscription_id,generation,turn_id,text) VALUES($1,1,$2,'Delayed result')`, threadSub, work.TurnID); err != nil {
					t.Fatal(err)
				}
				result, err := store.ClaimThreadDelivery(ctx, "delayed-result")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.FinishThreadDelivery(ctx, result, true); err != nil {
					t.Fatal("historical result remained in retry loop", err)
				}
				if err := pool.QueryRow(ctx, `SELECT d.state,s.enabled FROM runtime.thread_deliveries d JOIN runtime.thread_subscriptions s ON s.id=d.subscription_id WHERE d.id=$1`, result.ID).Scan(&state, &enabled); err != nil || state != "skipped" || enabled {
					t.Fatal("historical result subscription remained live", state, enabled, err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE thread_id=$1`, target).Scan(&inputs); err != nil || inputs != 0 {
					t.Fatal("historical purpose admitted a Thread result", inputs, err)
				}
				return
			}
			if state != "delivered" || !enabled {
				t.Fatal("live Calendar subscription disabled", state, enabled)
			}
			var input string
			if err := pool.QueryRow(ctx, `SELECT id FROM runtime.inputs WHERE thread_id=$1 AND source->>'kind'='observation'`, target).Scan(&input); err != nil {
				t.Fatal(err)
			}
			if got, err := store.ThreadApplication(ctx, scope, target); err != nil || got == nil || got.ID != job.ID {
				t.Fatal("observation lost original Calendar authority", got, err)
			}
			next, err := store.BeginTurn(ctx, lease, scope, input, runtimeConfig())
			if err != nil {
				t.Fatal(err)
			}
			last, err := store.BeginAttempt(ctx, lease, next.TurnID, managedruntime.ModelRequest{Messages: next.History})
			if err != nil {
				t.Fatal("Calendar observation cannot call its model", err)
			}
			call.ToolUseID, call.ToolName = "context", "read_context"
			if err := store.FinishAttempt(ctx, lease, last.ID, llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{call}}}, ""); err != nil {
				t.Fatal(err)
			}
			tool, err = store.ClaimTool(ctx, "context")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishTool(ctx, tool, managedruntime.ToolOutcome{State: "ready", Content: "context"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginTurn(ctx, lease, scope, input, runtimeConfig()); err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginAttempt(ctx, lease, next.TurnID, managedruntime.ModelRequest{Messages: next.History}); !errors.Is(err, managedruntime.ErrApplicationBudget) {
				t.Fatal("observation obtained a fresh application budget", err)
			}
			var total, calendar int
			if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE kind='calendar') FROM runtime.usage_records WHERE agent_id=$1`, scope.AgentID).Scan(&total, &calendar); err != nil || total != 3 || calendar != total {
				t.Fatal("application usage lost its purpose", total, calendar, err)
			}
		})
	}
}
