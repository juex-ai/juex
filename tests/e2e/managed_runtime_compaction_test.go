//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func compactionRequest(t *testing.T, store *runtimepg.Store, lease managedruntime.Lease, work managedruntime.Work) managedruntime.ModelRequest {
	t.Helper()
	job, err := store.PrepareCompaction(context.Background(), lease, work, "automatic", "")
	if err != nil {
		t.Fatal(err)
	}
	retained := work.History[len(work.History)-1:]
	return managedruntime.ModelRequest{Generation: work.Generation, Model: work.Config.Models[work.ModelIndex], Purpose: "compaction", MaxOutputTokens: 512, Messages: work.History, Compaction: &managedruntime.CompactionDraft{JobID: job.ID, SourceGeneration: work.Generation, SourceSequence: work.ContextSequence, BeforeTokens: llm.EstimateMessageTokens(work.History), Retained: retained, RetainedIDs: []string{retained[0].ID}}}
}

func seedCompaction(t *testing.T, store *runtimepg.Store, scope managedruntime.Scope, lease managedruntime.Lease, plans ...managedruntime.TurnConfig) managedruntime.Work {
	t.Helper()
	ctx := context.Background()
	config := runtimeConfig()
	if len(plans) > 0 {
		config = plans[0]
	}
	for i := 0; i < 3; i++ {
		input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: fmt.Sprint("history-", i), Text: strings.Repeat("Historical verified facts. ", 600)})
		if err != nil {
			t.Fatal(err)
		}
		work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Purpose: "conversation"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Completed earlier work."), StopReason: llm.StopEndTurn}, ""); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "current", Text: "Continue the current task"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	return work
}

func TestManagedRuntimeCompactionCheckpointSurvivesRestartAndQueuedInput(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "compactor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work := seedCompaction(t, store, scope, lease)
	request := compactionRequest(t, store, lease, work)
	queued, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "next", Text: "This queued input must not enter the summary"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareCompaction(ctx, lease, work, "automatic", ""); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("prepared during provider attempt", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.Claim(ctx, scope.AgentID, "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := runtimepg.New(pool).BeginTurn(ctx, replacement, scope, work.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Compaction == nil || recovered.Compaction.ID != request.Compaction.JobID || recovered.Compaction.Attempts != 1 || recovered.ContextSequence != work.ContextSequence {
		t.Fatal("lost compaction source", recovered)
	}
	if _, err := store.BeginAttempt(ctx, replacement, work.TurnID, managedruntime.ModelRequest{Purpose: "conversation"}); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("bypassed pending compaction", err)
	}
	retried, err := store.BeginAttempt(ctx, replacement, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Earlier work completed. Continue the current task."), StopReason: llm.StopEndTurn, UsageStatus: llm.UsageComplete, Usage: llm.Usage{InputTokens: 200, OutputTokens: 20}}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, response, ""); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("old activation overwrote checkpoint", err)
	}
	if err := store.FinishAttempt(ctx, replacement, retried.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, replacement, retried.ID, response, ""); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("duplicate summary applied", err)
	}
	current, err := runtimepg.New(pool).BeginTurn(ctx, replacement, scope, work.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation != 2 || current.Compaction != nil || len(current.History) != 2 || current.History[0].Kind != llm.MessageKindCompact || current.History[1].ID != work.InputID {
		t.Fatal("incorrect recovered checkpoint", current)
	}
	for _, message := range current.History {
		if message.ID == queued.ID {
			t.Fatal("consumed next input")
		}
	}
	page, err := store.ReadContext(ctx, scope, main.ID, managedruntime.ContextReference(work.History[0].ID, 0, "text"), 0, 100)
	if err != nil || !strings.Contains(page.Text, "Historical verified") {
		t.Fatal("original history lost", page, err)
	}
	nextAttempt, err := store.BeginAttempt(ctx, replacement, current.TurnID, managedruntime.ModelRequest{Purpose: "conversation", Generation: current.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, replacement, nextAttempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Current work done")}, ""); err != nil {
		t.Fatal(err)
	}
	next, err := store.BeginTurn(ctx, replacement, scope, queued.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != 2 || len(next.History) != 4 || next.History[3].ID != queued.ID {
		t.Fatal("next Turn lost checkpoint", next)
	}
	var saved managedruntime.ModelRequest
	var raw []byte
	var state string
	if err := pool.QueryRow(ctx, `SELECT state,request FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil || state != "unknown" || saved.Generation != 1 || saved.MaxOutputTokens != 512 {
		t.Fatal("attempt budget/generation not immutable", saved, state, err)
	}
}

func TestManagedRuntimeCompactionInvalidSummaryAndCancellationKeepGeneration(t *testing.T) {
	for _, name := range []string{"truncated", "tool", "empty", "cancelled", "source_changed"} {
		t.Run(name, func(t *testing.T) {
			pool, store, scope, main := runtimeDatabase(t)
			ctx := context.Background()
			lease, err := store.Claim(ctx, scope.AgentID, "compactor", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			work := seedCompaction(t, store, scope, lease)
			request := compactionRequest(t, store, lease, work)
			attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
			if err != nil {
				t.Fatal(err)
			}
			response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Verified progress"), StopReason: llm.StopEndTurn, UsageStatus: llm.UsageComplete, Usage: llm.Usage{InputTokens: 200, OutputTokens: 20}}
			switch name {
			case "truncated":
				response.StopReason = llm.StopMaxTokens
			case "tool":
				response.Message.Blocks = append(response.Message.Blocks, llm.Block{Type: llm.BlockToolUse, ToolName: "write", ToolUseID: "forbidden"})
			case "empty":
				response.Message.Blocks = nil
			case "cancelled":
				if err := store.CancelThread(ctx, scope, main.ID); err != nil {
					t.Fatal(err)
				}
			case "source_changed":
				if _, err := pool.Exec(ctx, `UPDATE runtime.events SET sequence=sequence+10000 WHERE thread_id=$1 AND kind='message.appended' AND data->>'id'=$2`, main.ID, work.InputID); err != nil {
					t.Fatal(err)
				}
			}
			err = store.FinishAttempt(ctx, lease, attempt.ID, response, "")
			if name == "source_changed" {
				if !errors.Is(err, managedruntime.ErrConflict) {
					t.Fatal("stale source applied", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var gen int64
			var checkpoints int
			if err := pool.QueryRow(ctx, `SELECT generation,(SELECT count(*) FROM runtime.context_checkpoints WHERE thread_id=$1) FROM runtime.threads WHERE id=$1`, main.ID).Scan(&gen, &checkpoints); err != nil || gen != 1 || checkpoints != 0 {
				t.Fatal("failed summary changed context", gen, checkpoints, err)
			}
			if name != "source_changed" {
				var status string
				if err := pool.QueryRow(ctx, `SELECT usage_status FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&status); err != nil || status != "complete" {
					t.Fatal("lost failed summary usage", status, err)
				}
			}
			var attention int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.notification_outbox WHERE event->>'kind'='attention'`).Scan(&attention); err != nil {
				t.Fatal(err)
			}
			wantAttention := 1
			if name == "cancelled" || name == "source_changed" {
				wantAttention = 0
			}
			if attention != wantAttention {
				t.Fatal("compaction failure notification count", attention, wantAttention)
			}
		})
	}
}

func TestManagedRuntimeAutomaticCompactionContinuesOriginalTurn(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint("manual=", manual), func(t *testing.T) {
			var calls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				encoded, _ := json.Marshal(body["messages"])
				if calls.Add(1) == 1 {
					if !strings.Contains(string(encoded), "Summarize this conversation") || len(body["tools"].([]any)) != 0 || body["max_completion_tokens"] != float64(1000) {
						t.Error("summary request has conversation tools or budget", body["tools"], body["max_completion_tokens"])
					}
					streamManagedReply(w, "Historical work completed. Current task: reply after compaction.")
				} else {
					if !strings.Contains(string(encoded), "Historical work completed.") || !strings.Contains(string(encoded), "reply after compaction") || body["tools"] == nil {
						t.Error("conversation did not resume checkpoint")
					}
					streamManagedReply(w, "Continued original task")
				}
			})
			ctx := context.Background()
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			config, err := f.authority.Snapshot(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := f.store.Claim(ctx, scope.AgentID, "seed-history", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 18; i++ {
				input, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: fmt.Sprint("prior-", i), Text: strings.Repeat("Historical detailed facts. ", 800)})
				if err != nil {
					t.Fatal(err)
				}
				work, err := f.store.BeginTurn(ctx, lease, scope, input.ID, config)
				if err != nil {
					t.Fatal(err)
				}
				attempt, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Purpose: "conversation"})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Done"), StopReason: llm.StopEndTurn}, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Release(ctx, lease); err != nil {
				t.Fatal(err)
			}
			var input managedruntime.InputReceipt
			expectedCalls := int32(2)
			if manual {
				expectedCalls = 1
				input = managementCall[managedruntime.InputReceipt](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/compact", f.origin, managedruntime.CompactionRequest{RequestID: "manual-summary", Focus: "Keep exact file identifiers"}, 200)
			} else {
				input = f.submit(t, "after-long-history", f.main.ID, "reply after compaction")
			}
			f.run(t)
			runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == expectedCalls })
			var generation int64
			var requests, known int
			if err := f.pool.QueryRow(ctx, `SELECT t.generation,count(a.id),count(a.id) FILTER(WHERE a.usage_status='complete') FROM runtime.turns t JOIN runtime.attempts a ON a.turn_id=t.id WHERE t.input_id=$1 GROUP BY t.generation`, input.ID).Scan(&generation, &requests, &known); err != nil || generation != 2 || requests != int(expectedCalls) || known != int(expectedCalls) {
				t.Fatal("compaction lost Turn/usage", generation, requests, known, err)
			}
		})
	}
}

func TestManagedRuntimeManualCompactionIsDurableControl(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		streamManagedReply(w, "Unexpected provider call")
	})
	request := managedruntime.CompactionRequest{RequestID: "manual", Focus: "Keep exact identifiers"}
	first := managementCall[managedruntime.InputReceipt](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/compact", f.origin, request, 200)
	second := managementCall[managedruntime.InputReceipt](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/compact", f.origin, request, 200)
	if first.ID != second.ID {
		t.Fatal("duplicate manual command")
	}
	request.Focus = "Different intent"
	managementCall[map[string]any](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/compact", f.origin, request, 409)
	managementCall[map[string]any](t, f.client, "POST", f.base+"/inputs", f.origin, managedruntime.InputRequest{RequestID: "manual", ThreadID: f.main.ID, Text: "ordinary message"}, 409)
	f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	if calls.Load() != 0 {
		t.Fatal("empty context consumed provider call")
	}
	timeline := f.timeline(t, f.main.ID)
	unchanged := false
	for _, event := range timeline.Events {
		if event.Kind == "message.appended" {
			t.Fatal("manual command entered model history")
		}
		unchanged = unchanged || event.Kind == "context.unchanged"
	}
	if !unchanged || timeline.Thread.Generation != 1 {
		t.Fatal("missing no-op result", timeline)
	}
}

func TestManagedRuntimeCompactionFallbackRetainsJobAndActualUsage(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	backup := config.Models[0]
	backup.ModelID = "00000000-0000-4000-8000-000000000002"
	backup.Model = "backup"
	config.Models = append(config.Models, backup)
	work := seedCompaction(t, store, scope, lease, config)
	request := compactionRequest(t, store, lease, work)
	first, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, first.ID, llm.Response{UsageStatus: llm.UsagePartial, Usage: llm.Usage{InputTokens: 13, OutputTokens: 4}}, "provider_fallback"); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	lease, err = store.Claim(ctx, scope.AgentID, "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, work.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if work.Compaction == nil || work.Compaction.ID != request.Compaction.JobID || work.Compaction.Attempts != 1 || work.ModelIndex != 1 || work.Generation != 1 {
		t.Fatal("summary fallback lost original job", work)
	}
	request.Model = backup
	second, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, second.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Prior work complete. Continue the current input."), StopReason: llm.StopEndTurn, UsageStatus: llm.UsageComplete, Usage: llm.Usage{InputTokens: 200, OutputTokens: 20}}, ""); err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, work.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if work.Generation != 2 || work.History[0].Model != "fixture:backup" || work.ModelIndex != 1 {
		t.Fatal("summary model not retained", work)
	}
	var partial, complete int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE request->'model'->>'model'='small' AND usage_status='partial'),count(*) FILTER(WHERE request->'model'->>'model'='backup' AND usage_status='complete') FROM runtime.attempts WHERE turn_id=$1`, work.TurnID).Scan(&partial, &complete); err != nil || partial != 1 || complete != 1 {
		t.Fatal("fallback usage attributed incorrectly", partial, complete, err)
	}
	var calls, tokens int
	if err := pool.QueryRow(ctx, `SELECT count(*),sum(input_tokens+output_tokens) FROM runtime.usage_records WHERE kind='compaction'`).Scan(&calls, &tokens); err != nil || calls != 2 || tokens != 237 {
		t.Fatal("compaction fallback missing from usage ledger", calls, tokens, err)
	}
}

func TestManagedRuntimeCompactionRecoveryAttemptsAreBounded(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "compactor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work := seedCompaction(t, store, scope, lease)
	request := compactionRequest(t, store, lease, work)
	for i := 0; i < 6; i++ {
		if _, err := store.BeginAttempt(ctx, lease, work.TurnID, request); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, scope.AgentID); err != nil {
			t.Fatal(err)
		}
		lease, err = store.Claim(ctx, scope.AgentID, fmt.Sprint("replacement-", i), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		work, err = store.BeginTurn(ctx, lease, scope, work.InputID, managedruntime.TurnConfig{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BeginAttempt(ctx, lease, work.TurnID, request); !errors.Is(err, managedruntime.ErrCompactionFailed) {
		t.Fatal("unbounded summary requests", err)
	}
	if err := store.HoldInput(ctx, lease, work.InputID, "compaction_failed"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts WHERE turn_id=$1 AND state='unknown'`, work.TurnID).Scan(&count); err != nil || count != 6 {
		t.Fatal("lost unknown summary accounting", count, err)
	}
}
