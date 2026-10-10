//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedInputTrackingRunnerProjectsAndRefreshesChecklistOnFallback(t *testing.T) {
	var calls atomic.Int32
	var inputID string
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		system := ""
		for _, m := range request.Messages {
			if m.Role == "system" {
				var text string
				if json.Unmarshal(m.Content, &text) == nil {
					system += text
				}
			}
		}
		if calls.Add(1) == 1 {
			if !strings.Contains(system, inputID) || !strings.Contains(system, managedruntime.ContextReference(inputID, 0, "text")) {
				t.Error("provider did not receive authoritative identity/reference")
			}
			streamManagedTool(w, "check_inputs", map[string]any{"input_ids": []string{inputID}})
		} else {
			if strings.Contains(system, inputID) {
				t.Error("checked input remained in fresh system checklist")
			}
			streamManagedReply(w, "Handled and checked")
		}
	})
	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("undersized candidate reached provider")
		http.Error(w, "unexpected", 500)
	}))
	t.Cleanup(small.Close)
	ctx := context.Background()
	model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "tiny", Name: "tiny", Protocol: llm.ProtocolOpenAIChat, Endpoint: small.URL, APIKey: "fixture", ContextWindow: 2048, MaxOutput: 512, OutputReserve: 512, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.configureModels(ctx, []string{model.ID, f.agent.Configuration.Models[0]}); err != nil {
		t.Fatal(err)
	}
	input := f.submit(t, "tracking-provider", f.main.ID, "Confirm handling with the checklist")
	inputID = input.ID
	f.run(t)
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state) == nil && state == "completed"
	})
	if calls.Load() != 2 {
		t.Fatal("unexpected completion continuation", calls.Load())
	}
	page := managementCall[managedruntime.InputCheckPage](t, f.client, http.MethodPost, f.base+"/threads/"+f.main.ID+"/input-checks", f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{inputID}}, http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].CheckedAt == nil {
		t.Fatal("provider check not persisted", page)
	}
	inspection := managementCall[managedruntime.ThreadInspection](t, f.client, http.MethodGet, f.base+"/threads/"+f.main.ID+"/inspection", f.origin, nil, http.StatusOK)
	if inspection.LatestRequest == nil || inspection.LatestRequest.Model == "tiny" || len(inspection.InputChecklist.Items) != 0 {
		t.Fatal("actual request/inspection diverged", inspection)
	}
}

func TestManagedInputTrackingExplicitCheckFencesAndReset(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "tracking", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "first", Text: strings.Repeat("keep this constraint ", 3000)})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, first.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	originalScope := work.InputScopeID
	if len(work.InputReminders) != 1 || len([]rune(work.InputReminders[0].Message.FirstText())) > 256 {
		t.Fatal("unbounded/missing reminder", work.InputReminders)
	}
	finishStateTurn(t, store, lease, work)
	inspection, err := store.Inspection(ctx, scope, main.ID)
	if err != nil || len(inspection.InputChecklist.Items) != 1 || inspection.InputChecklist.Items[0].CheckedAt != nil || inspection.Thread.PendingInputs != 0 {
		t.Fatal("completion checked or continued input", inspection, err)
	}
	next, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "next", Text: "Now confirm"})
	if err != nil {
		t.Fatal(err)
	}
	work, err = runtimepg.New(pool).BeginTurn(ctx, lease, scope, next.ID, runtimeConfig())
	if err != nil || len(work.InputReminders) != 2 {
		t.Fatal("restart lost checklist", err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "premature", ToolName: "check_inputs", Input: map[string]any{"input_ids": []string{first.ID}}}, llm.Block{ToolUseID: "notes", ToolName: "update_notes", Input: map[string]any{"content": "still pending"}})
	job, err := store.ClaimTool(ctx, "tracking")
	if err != nil {
		t.Fatal(err)
	}
	action, err := managedruntime.ParseThreadStateAction(job.Call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyThreadStateAction(ctx, job, action); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("same-response check accepted", err)
	}
	if err = store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "ready", IsError: true, Content: "wait for previous response results"}); err != nil {
		t.Fatal(err)
	}
	applyStateTool(t, store, "update_notes")
	work, err = store.BeginTurn(ctx, lease, scope, next.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "check", ToolName: "check_inputs", Input: map[string]any{"input_ids": []string{first.ID, first.ID}}})
	job, err = store.ClaimTool(ctx, "tracking")
	if err != nil {
		t.Fatal(err)
	}
	action, err = managedruntime.ParseThreadStateAction(job.Call)
	if err != nil {
		t.Fatal(err)
	}
	denied := job
	denied.FrozenCapabilities.Disabled = []agentpolicy.Capability{agentpolicy.InputTracking}
	if _, err = store.ApplyThreadStateAction(ctx, denied, action); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("frozen capability expanded", err)
	}
	denied = job
	denied.Scope.Capabilities.Disabled = []agentpolicy.Capability{agentpolicy.InputTracking}
	if _, err = store.ApplyThreadStateAction(ctx, denied, action); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("current capability ignored", err)
	}
	bad := action
	bad.InputIDs = []string{first.ID, uuid.NewString()}
	if _, err = store.ApplyThreadStateAction(ctx, job, bad); !errors.Is(err, managedruntime.ErrInvalid) {
		t.Fatal("partial/foreign check accepted", err)
	}
	result, err := store.ApplyThreadStateAction(ctx, job, action)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = pool.QueryRow(ctx, `SELECT checked_at::text FROM runtime.input_tracking WHERE input_id=$1`, first.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyThreadStateAction(ctx, job, action); err != nil {
		t.Fatal("lost-reply replay", err)
	}
	var after string
	if err = pool.QueryRow(ctx, `SELECT checked_at::text FROM runtime.input_tracking WHERE input_id=$1`, first.ID).Scan(&after); err != nil || before != after {
		t.Fatal("repeat changed check evidence", err)
	}
	if err = store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "ready", Content: string(result)}); err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, next.ID, runtimeConfig())
	if err != nil || len(work.InputReminders) != 1 || work.InputReminders[0].InputID != next.ID {
		t.Fatal("stale reminder", err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "new", ToolName: "context_new", Input: map[string]any{}})
	resetJob, err := store.ClaimTool(ctx, "tracking")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyThreadStateAction(ctx, resetJob, managedruntime.ThreadStateAction{Kind: "context_new"}); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("model reset forgot unchecked input", err)
	}
	if err = store.FinishTool(ctx, resetJob, managedruntime.ToolOutcome{State: "ready", IsError: true, Content: "unchecked input remains"}); err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, next.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	finishStateTurn(t, store, lease, work)
	if _, err = store.ResetContext(ctx, scope, main.ID, "human-new"); err != nil {
		t.Fatal(err)
	}
	page, err := store.InputChecks(ctx, scope, main.ID, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID, next.ID}})
	if err != nil || len(page.Items) != 2 || page.ScopeID == originalScope || page.Items[0].CheckedAt == nil || page.Items[1].CheckedAt != nil || page.Items[1].ScopeID == page.ScopeID {
		t.Fatal("reset fabricated check or erased history", page, err)
	}
	if page.Items[0].CheckActionID != job.ID || page.Items[0].CheckMessageID == "" || page.Items[0].ToolUseID != "check" {
		t.Fatal("missing evidence", page)
	}
	inspection, err = store.Inspection(ctx, scope, main.ID)
	if err != nil || len(inspection.InputChecklist.Items) != 0 {
		t.Fatal("new scope not empty", err)
	}
}

func TestManagedInputTrackingCapacityDisabledAndReadHTTP(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var first managedruntime.InputReceipt
	for i := 0; i < managedruntime.MaxTrackedInputs; i++ {
		input, e := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: fmt.Sprintf("bounded-%d", i), Text: "pending"})
		if e != nil {
			t.Fatal(i, e)
		}
		if i == 0 {
			first = input
		}
	}
	if _, err = f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "overflow", Text: "must not commit"}); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("unbounded checklist", err)
	}
	var leaked int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE thread_id=$1 AND request_id='overflow'`, f.main.ID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("partial acceptance", leaked, err)
	}
	retry, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "bounded-0", Text: "pending"})
	if err != nil || retry.ID != first.ID {
		t.Fatal("full checklist broke exact retry", err)
	}
	disabled := scope
	disabled.Capabilities.Disabled = []agentpolicy.Capability{agentpolicy.InputTracking}
	untracked, err := f.store.AcceptInput(ctx, disabled, managedruntime.InputRequest{RequestID: "disabled", Text: "untracked"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := f.base + "/threads/" + f.main.ID + "/input-checks"
	page := managementCall[managedruntime.InputCheckPage](t, f.client, http.MethodPost, endpoint, f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID, untracked.ID}}, http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].InputID != first.ID || page.Items[0].Delivery != "registered" || page.Items[0].CheckedAt != nil {
		t.Fatal(page)
	}
	managementCall[any](t, f.client, http.MethodPost, endpoint, f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{"invalid"}}, http.StatusBadRequest)
	managementCall[any](t, f.client, http.MethodPost, endpoint, f.origin, managedruntime.InputCheckQuery{MessageIDs: make([]string, 501)}, http.StatusBadRequest)
	before, err := f.store.Inspection(ctx, scope, f.main.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		managementCall[managedruntime.InputCheckPage](t, f.client, http.MethodPost, endpoint, f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID}}, http.StatusOK)
	}
	after, err := f.store.Inspection(ctx, scope, f.main.ID)
	if err != nil || before.Thread.Sequence != after.Thread.Sequence || before.Thread.Generation != after.Thread.Generation {
		t.Fatal("read changed runtime", err)
	}
	worker, err := f.store.CreateWorker(ctx, scope, f.main.ID, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	other := managementCall[managedruntime.InputCheckPage](t, f.client, http.MethodPost, f.base+"/threads/"+worker.ID+"/input-checks", f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID}}, http.StatusOK)
	if len(other.Items) != 0 {
		t.Fatal("cross-thread leak", other)
	}
	managementCall[any](t, f.client, http.MethodPost, f.base+"/threads/"+uuid.NewString()+"/input-checks", f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID}}, http.StatusForbidden)
	foreign := scope
	foreign.AgentID = uuid.NewString()
	foreignMain, err := f.store.EnsureAgent(ctx, foreign)
	if err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, f.client, http.MethodPost, f.base+"/threads/"+foreignMain.ID+"/input-checks", f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID}}, http.StatusForbidden)
	if _, err = f.store.SetThreadArchived(ctx, scope, worker.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.DeleteThread(ctx, scope, worker.ID); err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, f.client, http.MethodPost, f.base+"/threads/"+worker.ID+"/input-checks", f.origin, managedruntime.InputCheckQuery{MessageIDs: []string{first.ID}}, http.StatusForbidden)
}

func TestManagedInputTrackingHookRejectionNeverBecomesDelivered(t *testing.T) {
	_, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "tracking", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "rejected", Text: "Must not be marked handled"})
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	config.Hooks = []hookpolicy.Declaration{managedHook("deny", uuid.NewString(), hookpolicy.UserPromptSubmit, "exit 2")}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil || !work.Deferred || len(work.InputReminders) != 0 {
		t.Fatal("Hook not awaited", work, err)
	}
	hook, err := store.ClaimHook(ctx, "tracking")
	if err != nil {
		t.Fatal(err)
	}
	exit := 2
	if err = store.FinishHook(ctx, hook, managedruntime.HookOutcome{State: "failed", ExitCode: &exit}); err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil || !work.Deferred {
		t.Fatal("Hook rejection lost", err)
	}
	page, err := store.InputChecks(ctx, scope, main.ID, managedruntime.InputCheckQuery{MessageIDs: []string{input.ID}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Delivery != "blocked" || page.Items[0].CheckedAt != nil {
		t.Fatal("blocked input fabricated delivery", page, err)
	}
	next, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "allowed", Text: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, next.ID, runtimeConfig())
	if err != nil || len(work.InputReminders) != 1 || work.InputReminders[0].InputID != next.ID {
		t.Fatal("rejected input entered reminders", err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolName: "check_inputs", ToolUseID: "invalid-check", Input: map[string]any{"input_ids": []string{input.ID}}})
	job, err := store.ClaimTool(ctx, "tracking")
	if err != nil {
		t.Fatal(err)
	}
	action, err := managedruntime.ParseThreadStateAction(job.Call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyThreadStateAction(ctx, job, action); !errors.Is(err, managedruntime.ErrInvalid) {
		t.Fatal("rejected input was checkable", err)
	}
}

func TestManagedInputTrackingCompactionKeepsScopeAndUncheckedInputs(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "tracking", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work := seedCompaction(t, store, scope, lease)
	original := work.InputScopeID
	request := compactionRequest(t, store, lease, work)
	request.Compaction.InputScopeID = original
	for _, item := range work.InputReminders {
		request.Compaction.UncheckedInputIDs = append(request.Compaction.UncheckedInputIDs, item.InputID)
	}
	if len(request.Compaction.UncheckedInputIDs) != 4 {
		t.Fatal("completed inputs were silently checked", request.Compaction.UncheckedInputIDs)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Input Checks\nAll handled.\n## Next Steps\nContinue"), StopReason: llm.StopEndTurn}, ""); err != nil {
		t.Fatal(err)
	}
	work, err = runtimepg.New(pool).BeginTurn(ctx, lease, scope, work.InputID, runtimeConfig())
	if err != nil || work.InputScopeID != original || work.Generation != 2 || len(work.InputReminders) != 4 {
		t.Fatal("compaction lost scope/checklist", work.InputScopeID, work.Generation, err)
	}
	if strings.Contains(work.History[0].FirstText(), "All handled.") || !strings.Contains(work.History[0].FirstText(), "unchecked_input_ids") {
		t.Fatal("summary overrode authoritative checklist", work.History[0])
	}
	check, err := store.Inspection(ctx, scope, main.ID)
	if err != nil || len(check.InputChecklist.Items) != 4 {
		t.Fatal(check, err)
	}
}
