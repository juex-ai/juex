package runtime

import (
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

func pendingQueueFixture(t *testing.T, now func() time.Time) (*PendingInputQueue, *thread.Thread) {
	t.Helper()
	store := thread.NewStore(t.TempDir())
	target, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Now: now, Thread: target}), target
}

func TestPendingInputQueueDeduplicatesAndReplaysInAcceptanceOrder(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue, _ := pendingQueueFixture(t, func() time.Time { return now })
	first, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "one"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "replacement"), PendingInputOptions{ID: "event-1"}, "turn-1"); err != nil {
		t.Fatal(err)
	} else if duplicate.Message.FirstText() != first.Message.FirstText() {
		t.Fatalf("duplicate replaced accepted input: %+v", duplicate)
	}
	now = now.Add(-time.Hour)
	second, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "two"), PendingInputOptions{ID: "event-2", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	replayable, err := queue.Replayable("turn-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 2 || replayable[0].ID != first.ID || replayable[1].ID != second.ID {
		t.Fatalf("replay order = %+v", replayable)
	}
}

func TestPendingInputQueueExpiryAndTerminalStateSurviveReplay(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue, target := pendingQueueFixture(t, func() time.Time { return now })
	record, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "stale"), PendingInputOptions{ID: "event-1", TTL: time.Second}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if replayable, err := queue.Replayable("turn-2", 0); err != nil || len(replayable) != 0 {
		t.Fatalf("expired replay = %+v, err=%v", replayable, err)
	}
	reloaded := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Now: func() time.Time { return now }, Thread: target})
	records, err := reloaded.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[record.ID].State != PendingInputStateExpired {
		t.Fatalf("state = %q, want expired", records[record.ID].State)
	}
}

func TestPendingInputQueueTurnAttemptAndCompletionUseThreadJournal(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 123456789, time.UTC)
	queue, target := pendingQueueFixture(t, func() time.Time { return now })
	record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "do it"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}
	input := target.ReplaySnapshot().Inputs[record.ID]
	if input == nil || input.State != thread.InputRunning || len(input.Attempts) != 1 || input.Attempts[0].State != "running" {
		t.Fatalf("input settled before consuming Turn: %+v", input)
	}
	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	replay := target.ReplaySnapshot()
	input = replay.Inputs[record.ID]
	if input == nil || input.State != thread.InputCompleted || len(input.Attempts) != 1 || input.Attempts[0].State != "succeeded" {
		t.Fatalf("input projection = %+v", input)
	}
	stored := replay.InputRecords[record.ID]
	if len(stored) == 0 {
		t.Fatal("input record missing from Thread journal")
	}
}

func TestPendingInputQueueTurnFailureDeadLettersAttempt(t *testing.T) {
	queue, target := pendingQueueFixture(t, time.Now)
	record, err := queue.AdmitTurnInput("turn-failed", llm.TextMessage(llm.RoleUser, "do it"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}
	if err := target.AppendEvent(events.Event{
		Type: "turn.errored", TurnID: "turn-failed", Payload: map[string]any{"error": "provider failed"},
	}); err != nil {
		t.Fatal(err)
	}
	replay := target.ReplaySnapshot()
	input := replay.Inputs[record.ID]
	if input == nil || input.State != thread.InputDeadLettered || len(input.Attempts) != 1 ||
		input.Attempts[0].State != "failed" || input.Attempts[0].Error != "provider failed" ||
		replay.Projection.Counts.PendingInputCount != 0 {
		t.Fatalf("failed input projection = %+v; counts=%+v", input, replay.Projection.Counts)
	}
}

func TestPendingInputQueueBulkTransitionsRemainRecoverable(t *testing.T) {
	queue, target := pendingQueueFixture(t, time.Now)
	ids := make([]string, 0, 90)
	for i := 0; i < 90; i++ {
		record, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "queued"), PendingInputOptions{TTL: time.Hour}, "turn-1")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.ID)
	}
	if err := queue.MarkDropped(ids); err != nil {
		t.Fatal(err)
	}
	reloaded := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target})
	records, err := reloaded.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if records[id].State != PendingInputStateDropped {
			t.Fatalf("record %s state = %q", id, records[id].State)
		}
	}
}
