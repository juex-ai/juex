package events

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBus_ExactMatch(t *testing.T) {
	b := NewBus()
	var got int32
	b.Subscribe("turn.started", func(e Event) { atomic.AddInt32(&got, 1) })
	if err := b.Emit(Event{Type: "turn.started"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Emit(Event{Type: "turn.completed"}); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestBus_DoesNotPublishFailedCommit(t *testing.T) {
	b := NewBus()
	want := errors.New("disk full")
	b.SetCommitter(failingCommitter{err: want})
	published := 0
	b.Subscribe("*", func(Event) { published++ })
	if err := b.Emit(Event{Type: "turn.started"}); !errors.Is(err, want) {
		t.Fatalf("Emit() error = %v, want %v", err, want)
	}
	if published != 0 {
		t.Fatalf("published = %d, want 0", published)
	}
}

func TestBus_CommitAndPublishCommittedAreSeparate(t *testing.T) {
	b := NewBus()
	published := 0
	b.Subscribe("turn.completed", func(Event) { published++ })

	committed, err := b.Commit(Event{Type: "turn.completed"})
	if err != nil {
		t.Fatal(err)
	}
	if committed.ID == "" || committed.Timestamp.IsZero() {
		t.Fatalf("committed event = %+v, want normalized identity", committed)
	}
	if published != 0 {
		t.Fatalf("published during Commit = %d, want 0", published)
	}
	b.PublishCommitted(committed)
	if published != 1 {
		t.Fatalf("published after PublishCommitted = %d, want 1", published)
	}
}

type failingCommitter struct{ err error }

func (c failingCommitter) Commit(Event) (Event, error) {
	return Event{}, c.err
}

func TestBus_GlobMatch(t *testing.T) {
	b := NewBus()
	var got int32
	b.Subscribe("tool.*", func(e Event) { atomic.AddInt32(&got, 1) })
	for _, eventType := range []string{"tool.requested", "tool.completed", "turn.started"} {
		if err := b.Emit(Event{Type: eventType}); err != nil {
			t.Fatal(err)
		}
	}
	if got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestBus_WildcardAll(t *testing.T) {
	b := NewBus()
	var got int32
	b.Subscribe("*", func(e Event) { atomic.AddInt32(&got, 1) })
	if err := b.Emit(Event{Type: "tool.requested"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Emit(Event{Type: "turn.started"}); err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	b := NewBus()
	var got int32
	unsubscribe := b.Subscribe("*", func(e Event) { atomic.AddInt32(&got, 1) })
	if err := b.Emit(Event{Type: "turn.started"}); err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	if err := b.Emit(Event{Type: "turn.completed"}); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestBus_AutoFillsIDAndTimestamp(t *testing.T) {
	b := NewBus()
	var captured Event
	var mu sync.Mutex
	b.Subscribe("*", func(e Event) {
		mu.Lock()
		captured = e
		mu.Unlock()
	})
	if err := b.Emit(Event{Type: "x"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if captured.ID == "" {
		t.Fatal("ID should be auto-filled")
	}
	if captured.Timestamp.IsZero() {
		t.Fatal("Timestamp should be auto-filled")
	}
	if captured.Timestamp.Location() != time.UTC {
		t.Fatalf("Timestamp location = %v, want UTC", captured.Timestamp.Location())
	}
	if captured.Timestamp.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("Timestamp = %s, want millisecond precision", captured.Timestamp.Format(time.RFC3339Nano))
	}
}

func TestNormalizeCanonicalizesTimestamp(t *testing.T) {
	input := time.Date(2026, 9, 1, 12, 34, 56, 987654321, time.FixedZone("test", 8*60*60))

	got := Normalize(Event{Type: "x", Timestamp: input})
	want := input.UTC().Truncate(time.Millisecond)
	if !got.Timestamp.Equal(want) || got.Timestamp.Location() != time.UTC {
		t.Fatalf("Timestamp = %s (%v), want %s (UTC)", got.Timestamp, got.Timestamp.Location(), want)
	}
}
