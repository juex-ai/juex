package runtime

import (
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func TestPendingInputQueue_DeduplicatesByID(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})

	first, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "one"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "two"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != first.ID || second.Message.FirstText() != "one" {
		t.Fatalf("duplicate enqueue replaced record: first=%+v second=%+v", first, second)
	}
	records, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "event-1" {
		t.Fatalf("records = %+v", records)
	}
}

func TestPendingInputQueue_ExpiresReplayableRecords(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "stale"), PendingInputOptions{ID: "event-1", TTL: time.Second}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Second)
	replayable, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 0 {
		t.Fatalf("replayable = %+v, want none", replayable)
	}
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[record.ID].State != PendingInputStateExpired {
		t.Fatalf("state = %q, want expired", records[record.ID].State)
	}
}

func TestPendingInputQueue_ProcessedRecordsDoNotReplay(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "done"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}

	replayable, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 0 {
		t.Fatalf("replayable = %+v, want none", replayable)
	}
}

func TestPendingInputQueue_PersistsStableMessageID(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "recover"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	reloaded := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	replayable, err := reloaded.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 {
		t.Fatalf("replayable = %+v", replayable)
	}
	if replayable[0].Message.ID == "" || replayable[0].Message.ID != record.Message.ID {
		t.Fatalf("message id = %q, want stable %q", replayable[0].Message.ID, record.Message.ID)
	}
}
