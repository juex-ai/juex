package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/homestore"
	"github.com/juex-ai/juex/internal/foundation/llm"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func trackedQueue(t *testing.T) (*PendingInputQueue, *thread.Thread) {
	t.Helper()
	_, target := pendingQueueFixture(t, nil)
	return NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true}), target
}

func deliverTracked(t *testing.T, queue *PendingInputQueue, text string) PendingInputRecord {
	t.Helper()
	record, err := queue.AdmitTurnInput("turn-"+text, llm.TextMessage(llm.RoleUser, text), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.thread.Append(record.Message); err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkMessageProcessed(record.Message); err != nil {
		t.Fatal(err)
	}
	return record
}

func settleTracked(t *testing.T, q *PendingInputQueue, record PendingInputRecord) {
	t.Helper()
	event := events.Event{Type: "turn.completed", TurnID: record.TurnID, Payload: TurnCompletedPayload{InputIDs: []string{record.ID}}}
	if err := q.thread.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	if err := q.ApplyTerminalEvent(event); err != nil {
		t.Fatal(err)
	}
}

func TestInputTrackingRetainsUncheckedWithoutRequeue(t *testing.T) {
	q, target := trackedQueue(t)
	first := deliverTracked(t, q, "original task")
	second := deliverTracked(t, q, "status question")
	settleTracked(t, q, first)
	settleTracked(t, q, second)
	confirmed, _, err := q.checkInputs(t.Context(), []string{second.ID, second.ID}, "answer-turn")
	if err != nil || !reflect.DeepEqual(confirmed, []string{second.ID}) {
		t.Fatalf("check = %v, %v", confirmed, err)
	}
	q = NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true})
	open, err := q.uncheckedRecords()
	if err != nil || len(open) != 1 || open[0].ID != first.ID || open[0].Message.FirstText() != "original task" {
		t.Fatalf("remaining = %+v, %v", open, err)
	}
	if replay, err := q.Replayable("", 0); err != nil || len(replay) != 0 {
		t.Fatalf("reminders were requeued: %+v, %v", replay, err)
	}
	assertThreadIndexPendingCount(t, target, 0)
	if _, _, err := q.checkInputs(t.Context(), []string{second.ID}, "retry"); err != nil {
		t.Fatalf("recheck after removal/reload: %v", err)
	}
}

func TestInputTrackingRecheckSurvivesRepeatedCompactionAndRestart(t *testing.T) {
	q, target := trackedQueue(t)
	checked := deliverTracked(t, q, "handled request")
	settleTracked(t, q, checked)
	if _, _, err := q.checkInputs(t.Context(), []string{checked.ID}, "check"); err != nil {
		t.Fatal(err)
	}
	for cycle := range 2 {
		later := deliverTracked(t, q, fmt.Sprintf("later request %d", cycle))
		settleTracked(t, q, later)
		if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "summary"), false, nil); err != nil {
			t.Fatal(err)
		}
		if err := target.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		target, err = thread.Load(target.Dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func(target *thread.Thread) { _ = target.Close() }(target)
		q = NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true})
		if _, event, err := q.checkInputs(t.Context(), []string{checked.ID}, "retry"); err != nil || event.Type != "" {
			t.Fatalf("cycle %d recheck lost idempotency: %+v, %v", cycle, event, err)
		}
		if replay, err := q.Replayable("", 0); err != nil || len(replay) != 0 {
			t.Fatalf("checks replayed work: %+v, %v", replay, err)
		}
		recorded, err := target.ReadEvents()
		if err != nil {
			t.Fatal(err)
		}
		checks := 0
		for _, event := range recorded {
			if event.Type == InputCheckedType {
				checks++
			}
		}
		if checks != 1 {
			t.Fatalf("recheck duplicated durable facts: %d", checks)
		}
	}
	if _, err := target.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "new-scope summary"), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	target, err = thread.Load(target.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	q = NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true})
	if _, _, err := q.checkInputs(t.Context(), []string{checked.ID}, "new scope"); err == nil {
		t.Fatal("previous-scope check was retained")
	}
}

func TestInputTrackingBatchValidationAndExecutionIndependence(t *testing.T) {
	q, _ := trackedQueue(t)
	first := deliverTracked(t, q, "do work")
	queued, err := q.Enqueue(llm.TextMessage(llm.RoleUser, "not delivered"), PendingInputOptions{}, first.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{queued.ID, "missing", ""} {
		if _, _, err := q.checkInputs(t.Context(), []string{first.ID, invalid}, "turn"); err == nil {
			t.Fatalf("accepted invalid batch with %q", invalid)
		}
		records, _ := q.Records()
		if records[first.ID].CheckedAt != nil {
			t.Fatal("partially committed invalid batch")
		}
	}
	if _, _, err := q.checkInputs(t.Context(), []string{first.ID}, "turn"); err != nil {
		t.Fatal(err)
	}
	records, _ := q.Records()
	if records[first.ID].State != PendingInputStateProcessed || records[first.ID].CheckedAt == nil || records[first.ID].Attempts != 1 {
		t.Fatalf("check mutated execution: %+v", records[first.ID])
	}
	settleTracked(t, q, first)
	records, _ = q.Records()
	if _, exists := records[first.ID]; exists || len(records) != 1 {
		t.Fatalf("settled checked record not removed: %+v", records)
	}
}

func TestInputTrackingReconcilesCheckBeforeStateWrite(t *testing.T) {
	q, target := trackedQueue(t)
	item := deliverTracked(t, q, "completed action")
	settleTracked(t, q, item)
	fail := true
	q.writeFile = func(path string, data []byte, fileMode, dirMode os.FileMode) error {
		if fail {
			return errors.New("injected state failure")
		}
		return homestore.WriteFileAtomic(path, data, fileMode, dirMode)
	}
	if _, _, err := q.checkInputs(t.Context(), []string{item.ID}, "turn"); err == nil {
		t.Fatal("expected state failure")
	}
	fail = false
	for _, reader := range []*PendingInputQueue{q, NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true})} {
		if open, err := reader.uncheckedRecords(); err != nil || len(open) != 0 {
			t.Fatalf("committed check lost: %+v, %v", open, err)
		}
		if _, _, err := reader.checkInputs(t.Context(), []string{item.ID}, "retry"); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err := target.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range recorded {
		if event.Type == InputCheckedType {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("check commits = %d", count)
	}
}

func TestInputTrackingDisableScopeAndCompaction(t *testing.T) {
	q, target := trackedQueue(t)
	old := deliverTracked(t, q, "keep across disable")
	settleTracked(t, q, old)
	disabled := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target})
	during := deliverTracked(t, disabled, "accepted while disabled")
	settleTracked(t, disabled, during)
	q = NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true})
	if open, err := q.uncheckedRecords(); err != nil || len(open) != 1 || open[0].ID != old.ID {
		t.Fatalf("reenabled = %+v, %v", open, err)
	}
	scope := target.ContextScopeID()
	if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "summary"), false, nil); err != nil {
		t.Fatal(err)
	}
	if target.ContextScopeID() != scope {
		t.Fatal("compaction changed scope")
	}
	if open, err := q.uncheckedRecords(); err != nil || len(open) != 1 {
		t.Fatalf("compact lost input: %+v, %v", open, err)
	}
	if _, err := target.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if open, err := q.uncheckedRecords(); err != nil || len(open) != 0 {
		t.Fatalf("same-process new retained old reminders: %+v, %v", open, err)
	}
	if _, _, err := q.checkInputs(t.Context(), []string{old.ID}, "new scope"); err == nil {
		t.Fatal("checked input outside scope")
	}
	newScope := target.ContextScopeID()
	if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "new summary"), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := thread.Load(target.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.ContextScopeID() != newScope || newScope == scope {
		t.Fatalf("new/compact/restart scope = %q, want %q", reopened.ContextScopeID(), newScope)
	}
}

func TestInputTrackingCapacityAndTTL(t *testing.T) {
	q, _ := trackedQueue(t)
	now := time.Now().UTC()
	q.now = func() time.Time { return now }
	for i := 0; i < maxTrackedInputs; i++ {
		if _, err := q.Enqueue(llm.TextMessage(llm.RoleUser, "keep"), PendingInputOptions{TTL: time.Second}, ""); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(24 * time.Hour)
	if replay, err := q.Replayable("", 0); err != nil || len(replay) != maxTrackedInputs {
		t.Fatalf("TTL erased accepted input: %d, %v", len(replay), err)
	}
	if _, err := q.Enqueue(llm.TextMessage(llm.RoleUser, "overflow"), PendingInputOptions{}, ""); err == nil {
		t.Fatal("capacity accepted input without space")
	}
}

func TestInputTrackingRestoresDeliveredProjectionAfterCrash(t *testing.T) {
	q, target := trackedQueue(t)
	item, err := q.AdmitTurnInput("turn", llm.TextMessage(llm.RoleUser, "accepted original"), false)
	if err != nil {
		t.Fatal(err)
	}
	projected := item.Message
	projected.Blocks = []llm.Block{{Type: llm.BlockText, Text: "trusted policy projection"}}
	if err := target.Append(projected); err != nil {
		t.Fatal(err)
	}
	// Crash after the transcript append, before processed state is written.
	event := events.Event{Type: "turn.errored", TurnID: "turn", Payload: TurnErroredPayload{InputIDs: []string{item.ID}, Error: "API failed", ErrorKind: "error"}}
	if err := target.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	reloaded := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: true})
	open, err := reloaded.uncheckedRecords()
	if err != nil || len(open) != 1 || open[0].Message.FirstText() != "accepted original" || open[0].ModelMessage.FirstText() != "trusted policy projection" {
		t.Fatalf("delivered state lost across failure: %+v, %v", open, err)
	}
}

func TestInputTrackingCheckDoesNotChangeFailedRecovery(t *testing.T) {
	for _, checked := range []bool{false, true} {
		t.Run(fmt.Sprint(checked), func(t *testing.T) {
			q, _ := trackedQueue(t)
			item := deliverTracked(t, q, "execute once")
			if checked {
				if _, _, err := q.checkInputs(t.Context(), []string{item.ID}, item.TurnID); err != nil {
					t.Fatal(err)
				}
			}
			event := events.Event{Type: "turn.errored", TurnID: item.TurnID, Payload: TurnErroredPayload{InputIDs: []string{item.ID}, Error: "restart", ErrorKind: "runtime_restart"}}
			if err := q.thread.AppendEvent(event); err != nil {
				t.Fatal(err)
			}
			if err := q.ApplyTerminalEvent(event); err != nil {
				t.Fatal(err)
			}
			if replay, err := q.Replayable("", 0); err != nil || len(replay) != 0 {
				t.Fatalf("restart handoff bypassed: %+v, %v", replay, err)
			}
			if count, err := q.RetryTurnInputs(item.TurnID); err != nil || count != 1 {
				t.Fatalf("check changed explicit retry: %d, %v", count, err)
			}
			if replay, err := q.Replayable("", 0); err != nil || len(replay) != 1 || (replay[0].CheckedAt != nil) != checked {
				t.Fatalf("retry lost input/check fact: %+v, %v", replay, err)
			}
		})
	}
}

func TestInputCheckPublicationAllowsSynchronousReadAndEnqueue(t *testing.T) {
	q, target := trackedQueue(t)
	item := deliverTracked(t, q, "finished")
	bus := events.NewBus()
	eng := &Engine{Thread: target, PendingInputQueue: q, TrackUserInputs: true, Bus: bus}
	bus.Subscribe("*", func(event events.Event) {
		if event.Type != InputCheckedType {
			return
		}
		if open, err := eng.UncheckedInputs(context.Background()); err != nil || len(open) != 0 {
			t.Errorf("published before check: %+v, %v", open, err)
		}
		if _, err := q.Enqueue(llm.TextMessage(llm.RoleUser, "next"), PendingInputOptions{}, ""); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := eng.CheckInputs(context.Background(), []string{item.ID}); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("check publication deadlocked")
	}
}

func TestInputTrackingRejectsCheckAlongsideOtherTools(t *testing.T) {
	q, target := trackedQueue(t)
	item := deliverTracked(t, q, "finish action")
	engine := &Engine{Thread: target, PendingInputQueue: q, TrackUserInputs: true}
	for _, mixed := range []bool{true, false} {
		message := llm.Message{ID: fmt.Sprintf("answer-%v", mixed), Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "check", ToolName: "check_inputs"}}}
		if mixed {
			message.Blocks = append(message.Blocks, llm.Block{Type: llm.BlockToolUse, ToolUseID: "action", ToolName: "write"})
		}
		if err := target.Append(message); err != nil {
			t.Fatal(err)
		}
		ctx := toolcore.WithToolCallEvents(t.Context(), toolcore.ToolCallEvents{Name: "check_inputs", ToolUseID: "check", MessageID: message.ID})
		_, err := engine.CheckInputs(ctx, []string{item.ID})
		if mixed && err == nil {
			t.Fatal("checked before other tools completed")
		}
		if !mixed && err != nil {
			t.Fatal(err)
		}
		records, _ := q.Records()
		if (records[item.ID].CheckedAt == nil) != mixed {
			t.Fatal("incorrect check state")
		}
	}
}
