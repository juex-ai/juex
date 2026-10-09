//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func stateToolBatch(t *testing.T, store *runtimepg.Store, lease managedruntime.Lease, work managedruntime.Work, calls ...llm.Block) {
	t.Helper()
	ctx := context.Background()
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: work.Config.Models[0].MaxOutput, Generation: work.Generation, Purpose: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range calls {
		calls[i].Type = llm.BlockToolUse
	}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: calls}, StopReason: llm.StopToolUse}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestManagedThreadStateTasksWaitForStopHooks(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "state", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "stop-tasks", Text: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	config.Hooks = []hookpolicy.Declaration{managedHook("stop", uuid.NewString(), hookpolicy.Stop, "true")}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "create", ToolName: "create_task", Input: map[string]any{"title": "Continue", "description": "Still todo"}})
	applyStateTool(t, store, "create_task")
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	finishStateTurn(t, store, lease, work)
	var state managedruntime.ThreadState
	if err := pool.QueryRow(ctx, `SELECT value FROM runtime.thread_state WHERE thread_id=$1`, main.ID).Scan(&state); err != nil || state.Tasks[0].ContinuationCount != 0 {
		t.Fatal("Tasks ran before Stop", state, err)
	}
	hook, err := store.ClaimHook(ctx, "stop")
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := store.FinishHook(ctx, hook, managedruntime.HookOutcome{State: "completed", ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil || !work.Deferred {
		t.Fatal("Stop did not settle", work, err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil || work.ThreadState.Tasks[0].ContinuationCount != 1 {
		t.Fatal("deferred finish skipped Tasks gate", work, err)
	}
	var hookContinuations int
	if err := pool.QueryRow(ctx, `SELECT hook_continuations FROM runtime.turns WHERE id=$1`, work.TurnID).Scan(&hookContinuations); err != nil || hookContinuations != 0 {
		t.Fatal("Tasks consumed Hook limit", hookContinuations, err)
	}
}

func TestManagedThreadStateWaitsForEveryWorkerDeliveryStage(t *testing.T) {
	for _, stage := range []string{"working", "delivery_pending", "input_queued"} {
		t.Run(stage, func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
			ctx := context.Background()
			scope, job := prepareRuntimeCall(t, f, f.main.ID, "delegate", llm.Block{Type: llm.BlockToolUse, ToolUseID: "worker", ToolName: "thread_create"})
			encoded, err := f.store.ApplyThreadAction(ctx, job, managedruntime.ThreadAction{Kind: "thread_create", Name: "Research", Query: "Worker task", Subscribe: true}, scope)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Thread  managedruntime.Thread       `json:"thread"`
				Receipt managedruntime.InputReceipt `json:"receipt"`
			}
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if err := f.store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "ready", Content: string(encoded)}); err != nil {
				t.Fatal(err)
			}
			state := managedruntime.ThreadState{Tasks: []managedruntime.ThreadTask{{ID: "parent-task", Title: "Wait", Description: "Read Worker result", Status: "doing", Priority: "p1", UpdatedAt: time.Now()}}}
			if _, err := f.pool.Exec(ctx, `INSERT INTO runtime.thread_state(thread_id,value) VALUES($1,$2)`, f.main.ID, state); err != nil {
				t.Fatal(err)
			}
			if stage != "working" {
				finishCollaborationInput(t, f, scope, result.Receipt.ID, "Verified Worker result")
			}
			if stage == "input_queued" {
				delivery, err := f.store.ClaimThreadDelivery(ctx, "deliver")
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishThreadDelivery(ctx, delivery, true); err != nil {
					t.Fatal(err)
				}
			}
			var input string
			if err := f.pool.QueryRow(ctx, `SELECT input_id FROM runtime.turns WHERE id=$1`, job.TurnID).Scan(&input); err != nil {
				t.Fatal(err)
			}
			finishCollaborationInput(t, f, scope, input, "Waiting for Worker")
			var current string
			if err := f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input).Scan(&current); err != nil || current != "completed" {
				t.Fatal("parent polled instead of deferring", current, err)
			}
			var saved managedruntime.ThreadState
			if err := f.pool.QueryRow(ctx, `SELECT value FROM runtime.thread_state WHERE thread_id=$1`, f.main.ID).Scan(&saved); err != nil || saved.Tasks[0].ContinuationCount != 0 {
				t.Fatal(saved, err)
			}
		})
	}
}

func TestManagedThreadStateResetRetainsKnownConnectionsAndRejectsUnknownEffects(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	scope, job := prepareRuntimeCall(t, f, f.main.ID, "connect", llm.Block{Type: llm.BlockToolUse, ToolUseID: "connection", ToolName: "mcp_connect"})
	if err := f.store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "ready", Content: "Connection established", OperationLive: true}); err != nil {
		t.Fatal(err)
	}
	var input string
	if err := f.pool.QueryRow(ctx, `SELECT input_id FROM runtime.turns WHERE id=$1`, job.TurnID).Scan(&input); err != nil {
		t.Fatal(err)
	}
	finishCollaborationInput(t, f, scope, input, "Connection stays open")
	if _, err := f.store.ResetContext(ctx, scope, f.main.ID, "keep-connection"); err != nil {
		t.Fatal("known live connection blocked reset", err)
	}
	var live bool
	if err := f.pool.QueryRow(ctx, `SELECT operation_live FROM runtime.tools WHERE id=$1`, job.ID).Scan(&live); err != nil || !live {
		t.Fatal("reset killed connection", live, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.tools SET state='unknown' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResetContext(ctx, scope, f.main.ID, "hide-unknown"); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("reset hid unknown external result", err)
	}
}

func TestManagedThreadStateProviderSeesFreshWorkingContext(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		// Tool arguments live in other message fields; the System content is the
		// provider-visible projection whose freshness this test checks.
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var system string
		for _, m := range request.Messages {
			if m.Role == "system" {
				system += m.Content
			}
		}
		switch calls.Add(1) {
		case 1:
			streamManagedTool(w, "update_notes", map[string]any{"content": "CURRENT_NOTE_641"})
		case 2:
			if !strings.Contains(system, "CURRENT_NOTE_641") {
				t.Error("new Notes missing from next provider call")
			}
			streamManagedTool(w, "create_task", map[string]any{"title": "CURRENT_TASK_827", "description": "Prove fresh projection"})
		case 3:
			match := regexp.MustCompile(`"id":"([^"]+)"`).FindStringSubmatch(system)
			if !strings.Contains(system, "CURRENT_NOTE_641") || !strings.Contains(system, "CURRENT_TASK_827") || len(match) != 2 {
				t.Error("fresh Tasks/Notes missing")
				streamManagedReply(w, "missing")
				return
			}
			streamManagedTool(w, "update_task", map[string]any{"id": match[1], "status": "done"})
		default:
			if strings.Contains(system, "CURRENT_NOTE_641") {
				t.Error("completed Tasks left stale Notes in provider context")
			}
			streamManagedReply(w, "Verified task complete")
		}
	})
	receipt := f.submit(t, "fresh-state", f.main.ID, "Work with Notes and Tasks")
	f.run(t)
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(context.Background(), `SELECT state FROM runtime.inputs WHERE id=$1`, receipt.ID).Scan(&state) == nil && state == "completed"
	})
	if calls.Load() != 4 {
		t.Fatal("unexpected model continuation", calls.Load())
	}
	reset := managementCall[managedruntime.Thread](t, f.client, http.MethodPost, f.base+"/threads/"+f.main.ID+"/reset-context", f.origin, map[string]string{"request_id": "reset-http"}, http.StatusOK)
	if reset.ID != f.main.ID || reset.Generation != 2 {
		t.Fatal(reset)
	}
}

func TestManagedThreadStateModelResetPreservesUnknownHookAndInstruction(t *testing.T) {
	for _, kind := range []string{"hook", "instruction"} {
		t.Run(kind, func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
			ctx := context.Background()
			scope, old := prepareRuntimeCall(t, f, f.main.ID, "old", llm.Block{Type: llm.BlockToolUse, ToolUseID: "old", ToolName: "list_tasks"})
			if err := f.store.FinishTool(ctx, old, managedruntime.ToolOutcome{State: "ready", Content: "[]"}); err != nil {
				t.Fatal(err)
			}
			var input string
			if err := f.pool.QueryRow(ctx, `SELECT input_id FROM runtime.turns WHERE id=$1`, old.TurnID).Scan(&input); err != nil {
				t.Fatal(err)
			}
			finishCollaborationInput(t, f, scope, input, "Old turn ended")
			if kind == "hook" {
				_, err := f.pool.Exec(ctx, `INSERT INTO runtime.hooks(turn_id,thread_id,event,anchor,hook_id,ordinal,scope,declaration,input,state,cancel_requested) VALUES($1,$2,'Stop','old','unknown-hook',0,$3,'{}','{}','unknown',true)`, old.TurnID, f.main.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := f.pool.Exec(ctx, `INSERT INTO runtime.instruction_preparations(turn_id,thread_id,scope,config,state,cancel_requested) VALUES($1,$2,$3,'{}','unknown',true)`, old.TurnID, f.main.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.ResetContext(ctx, scope, f.main.ID, "human"); !errors.Is(err, managedruntime.ErrConflict) {
				t.Fatal("human reset hid unknown effect", err)
			}
			_, work := prepareRuntimeCall(t, f, f.main.ID, "new", llm.Block{Type: llm.BlockToolUse, ToolUseID: "new", ToolName: "context_new"})
			if _, err := f.store.ApplyThreadStateAction(ctx, work, managedruntime.ThreadStateAction{Kind: "context_new"}); !errors.Is(err, managedruntime.ErrConflict) {
				t.Fatal("model reset hid unknown effect", err)
			}
			var generation int64
			if err := f.pool.QueryRow(ctx, `SELECT generation FROM runtime.threads WHERE id=$1`, f.main.ID).Scan(&generation); err != nil || generation != 1 {
				t.Fatal("rejected reset changed context", generation, err)
			}
			if kind == "hook" {
				if _, err := f.pool.Exec(ctx, `UPDATE runtime.hooks SET state='cancelled' WHERE turn_id=$1`, old.TurnID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.pool.Exec(ctx, `UPDATE runtime.instruction_preparations SET state='cancelled',output_acknowledged=true WHERE turn_id=$1`, old.TurnID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.ApplyThreadStateAction(ctx, work, managedruntime.ThreadStateAction{Kind: "context_new"}); err != nil {
				t.Fatal("confirmed old outcome still blocked reset", err)
			}
		})
	}
}

func TestManagedThreadStateContextToolSettlesWholeBatch(t *testing.T) {
	for _, kind := range []string{"context_new", "context_compact"} {
		t.Run(kind, func(t *testing.T) {
			pool, store, scope, main := runtimeDatabase(t)
			ctx := context.Background()
			lease, err := store.Claim(ctx, scope.AgentID, "state", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			work := seedCompaction(t, store, scope, lease)
			stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "transition", ToolName: kind, Input: map[string]any{}}, llm.Block{ToolUseID: "notes", ToolName: "update_notes", Input: map[string]any{"content": "after request, before transition"}})
			applyStateTool(t, store, kind)
			var generation int64
			if err := pool.QueryRow(ctx, `SELECT generation FROM runtime.threads WHERE id=$1`, main.ID).Scan(&generation); err != nil || generation != 1 {
				t.Fatal("early transition", generation, err)
			}
			applyStateTool(t, store, "update_notes")
			work, err = store.BeginTurn(ctx, lease, scope, work.InputID, runtimeConfig())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "context_new" {
				if work.Generation != 2 || work.ThreadState.Notes.Content != "" || len(work.History) != 1 {
					t.Fatal("reset did not settle after batch", work)
				}
			} else if work.Compaction == nil || work.ThreadState.Notes.Content == "" || work.Generation != 1 {
				t.Fatal("compaction not scheduled with current Notes", work)
			}
			if err := llm.ValidateToolTranscript(work.History); err != nil {
				t.Fatal("new context has orphan tool result", err)
			}
			var old []llm.Message
			rows, err := pool.Query(ctx, `SELECT data FROM runtime.events WHERE thread_id=$1 AND generation=1 AND kind='message.appended' ORDER BY sequence`, main.ID)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var m llm.Message
				if err := rows.Scan(&m); err != nil {
					t.Fatal(err)
				}
				old = append(old, m)
			}
			rows.Close()
			if err := llm.ValidateToolTranscript(old); err != nil {
				t.Fatal("historical batch incomplete", err)
			}
		})
	}
}

func TestManagedThreadStateCompactionCommitsOnlyOnSuccess(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			pool, store, scope, main := runtimeDatabase(t)
			ctx := context.Background()
			lease, err := store.Claim(ctx, scope.AgentID, "state", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			work := seedCompaction(t, store, scope, lease)
			state := managedruntime.ThreadState{Revision: 3, Notes: managedruntime.ThreadNotes{Content: "- [ ] KEEP_938"}, Tasks: []managedruntime.ThreadTask{
				{ID: "pending", Title: "Pending", Description: "Needs reply", Status: "pending", Priority: "p0", UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)},
				{ID: "done", Title: "Done", Description: "Verified", Status: "done", Priority: "p1", UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)},
			}}
			if _, err := pool.Exec(ctx, `INSERT INTO runtime.thread_state(thread_id,value) VALUES($1,$2)`, main.ID, state); err != nil {
				t.Fatal(err)
			}
			request := compactionRequest(t, store, lease, work)
			request.Compaction.ThreadState = state
			request.Compaction.NotesEnabled = true
			request.Compaction.TasksEnabled = true
			attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
			if err != nil {
				t.Fatal(err)
			}
			failure := ""
			if outcome == "failed" {
				failure = "provider_error"
			}
			if outcome == "cancelled" {
				if err := store.CancelThread(ctx, scope, main.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Tasks\nSummary\n## Next Steps\nContinue"), StopReason: llm.StopEndTurn}, failure); err != nil {
				t.Fatal(err)
			}
			var got managedruntime.ThreadState
			if err := pool.QueryRow(ctx, `SELECT value FROM runtime.thread_state WHERE thread_id=$1`, main.ID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if outcome == "success" {
				if len(got.Tasks) != 1 || got.Tasks[0].ID != "pending" || got.Notes.Content != state.Notes.Content || got.Revision != 4 {
					t.Fatal("bad success state", got)
				}
			} else if !reflect.DeepEqual(got, state) {
				t.Fatal("failed/cancelled compaction changed state", got)
			}
		})
	}
}

func applyStateTool(t *testing.T, store *runtimepg.Store, name string) (managedruntime.ToolWork, json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	work, err := store.ClaimTool(ctx, "state-test")
	if err != nil || work.Call.ToolName != name {
		t.Fatalf("claim %s: %s %v", name, work.Call.ToolName, err)
	}
	action, err := managedruntime.ParseThreadStateAction(work.Call)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyThreadStateAction(ctx, work, action)
	if err != nil {
		t.Fatal(err)
	}
	// A lost reply must replay the durable receipt, including its original ID.
	again, err := store.ApplyThreadStateAction(ctx, work, action)
	var oldValue, newValue any
	if err != nil || json.Unmarshal(again, &oldValue) != nil || json.Unmarshal(result, &newValue) != nil || !reflect.DeepEqual(oldValue, newValue) {
		t.Fatal("state action was repeated", err)
	}
	if err := store.FinishTool(ctx, work, managedruntime.ToolOutcome{State: "ready", Content: string(result)}); err != nil {
		t.Fatal(err)
	}
	return work, result
}

func finishStateTurn(t *testing.T, store *runtimepg.Store, lease managedruntime.Lease, work managedruntime.Work) {
	t.Helper()
	ctx := context.Background()
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: work.Config.Models[0].MaxOutput, Generation: work.Generation, Purpose: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Finished"), StopReason: llm.StopEndTurn}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestManagedThreadStateToolsOrderReceiptContinuationAndReset(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "state", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "state", Text: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	stateToolBatch(t, store, lease, work,
		llm.Block{ToolUseID: "notes", ToolName: "update_notes", Input: map[string]any{"content": "- [ ] Keep this working fact"}},
		llm.Block{ToolUseID: "create", ToolName: "create_task", Input: map[string]any{"title": "Verify", "description": "Prove behavior", "acceptance": "Tests pass"}},
		llm.Block{ToolUseID: "list", ToolName: "list_tasks", Input: map[string]any{}},
	)
	first, err := store.ClaimTool(ctx, "first")
	if err != nil || first.Call.ToolName != "update_notes" {
		t.Fatal("wrong ordinal", first, err)
	}
	if _, err := store.ClaimTool(ctx, "second"); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("state tools ran out of order", err)
	}
	if err := store.ReleaseToolClaims(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	applyStateTool(t, store, "update_notes")
	_, created := applyStateTool(t, store, "create_task")
	var result struct {
		Task managedruntime.ThreadTask `json:"task"`
	}
	if err := json.Unmarshal(created, &result); err != nil {
		t.Fatal(err)
	}
	applyStateTool(t, store, "list_tasks")
	work, err = runtimepg.New(pool).BeginTurn(ctx, lease, scope, input.ID, managedruntime.TurnConfig{})
	if err != nil || len(work.ThreadState.Tasks) != 1 || work.ThreadState.Notes.Content == "" {
		t.Fatal("state did not survive reload", work.ThreadState, err)
	}
	finishStateTurn(t, store, lease, work)
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, managedruntime.TurnConfig{})
	if err != nil || work.ThreadState.Tasks[0].ContinuationCount != 1 {
		t.Fatal("completion gate did not continue once", err, work.ThreadState)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "done", ToolName: "update_task", Input: map[string]any{"id": result.Task.ID, "status": "done"}})
	applyStateTool(t, store, "update_task")
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, managedruntime.TurnConfig{})
	if err != nil || work.ThreadState.Notes.Content != "" {
		t.Fatal("all-done did not clear Notes", err)
	}
	finishStateTurn(t, store, lease, work)
	reset, err := store.ResetContext(ctx, scope, main.ID, "reset-once")
	if err != nil || reset.ID != main.ID || reset.Generation != main.Generation+1 {
		t.Fatal("reset identity", reset, err)
	}
	again, err := store.ResetContext(ctx, scope, main.ID, "reset-once")
	if err != nil {
		t.Fatal("reset replay", err)
	}
	// PostgreSQL and JSON can represent the same UTC instant with different
	// time.Location objects. The persisted public receipt must remain identical.
	wantReceipt, err := json.Marshal(reset)
	if err != nil {
		t.Fatal(err)
	}
	gotReceipt, err := json.Marshal(again)
	if err != nil || string(gotReceipt) != string(wantReceipt) {
		t.Fatalf("reset replay: got %s, want %s, error %v", gotReceipt, wantReceipt, err)
	}
	input, err = store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "after-reset", Text: "Fresh context"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResetContext(ctx, scope, main.ID, "busy"); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("reset discarded queued input", err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil || len(work.ThreadState.Tasks) != 0 || len(work.History) != 1 || work.History[0].FirstText() != "Fresh context" {
		t.Fatal("context not reset", work, err)
	}
	var historical int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND generation=$2`, main.ID, main.Generation).Scan(&historical); err != nil || historical < 6 {
		t.Fatal("history erased", historical, err)
	}
}

func TestManagedThreadStateQueuedInputDefersTasksAndDisabledPolicyFencesTools(t *testing.T) {
	_, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "state", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "tasks", Text: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "create", ToolName: "create_task", Input: map[string]any{"title": "Wait", "description": "Needs input"}})
	applyStateTool(t, store, "create_task")
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "queued-result", Text: "Newly delivered result"})
	if err != nil {
		t.Fatal(err)
	}
	finishStateTurn(t, store, lease, work)
	work, err = store.BeginTurn(ctx, lease, scope, next.ID, runtimeConfig())
	if err != nil || work.ThreadState.Tasks[0].ContinuationCount != 0 {
		t.Fatal("queued input starved", work.ThreadState, err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "notes", ToolName: "update_notes", Input: map[string]any{"content": "forged"}})
	tool, err := store.ClaimTool(ctx, "forged")
	if err != nil {
		t.Fatal(err)
	}
	action, err := managedruntime.ParseThreadStateAction(tool.Call)
	if err != nil {
		t.Fatal(err)
	}
	tool.FrozenCapabilities.Disabled = []agentpolicy.Capability{agentpolicy.Notes}
	if _, err := store.ApplyThreadStateAction(ctx, tool, action); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("frozen policy bypass", err)
	}
	tool.FrozenCapabilities.Disabled = nil
	tool.Scope.Capabilities.Disabled = []agentpolicy.Capability{agentpolicy.Notes}
	if _, err := store.ApplyThreadStateAction(ctx, tool, action); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("current policy bypass", err)
	}
	tool.Scope.Capabilities.Disabled = nil
	if err := store.CancelThread(ctx, scope, work.ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyThreadStateAction(ctx, tool, action); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("cancelled tool mutated state", err)
	}
}
