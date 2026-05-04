package events

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestBus_ExactMatch(t *testing.T) {
	b := NewBus()
	var got int32
	b.Subscribe("turn.started", func(e Event) { atomic.AddInt32(&got, 1) })
	b.Emit(Event{Type: "turn.started"})
	b.Emit(Event{Type: "turn.completed"})
	if got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestBus_GlobMatch(t *testing.T) {
	b := NewBus()
	var got int32
	b.Subscribe("tool.*", func(e Event) { atomic.AddInt32(&got, 1) })
	b.Emit(Event{Type: "tool.requested"})
	b.Emit(Event{Type: "tool.completed"})
	b.Emit(Event{Type: "turn.started"})
	if got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestBus_WildcardAll(t *testing.T) {
	b := NewBus()
	var got int32
	b.Subscribe("*", func(e Event) { atomic.AddInt32(&got, 1) })
	b.Emit(Event{Type: "tool.requested"})
	b.Emit(Event{Type: "turn.started"})
	if got != 2 {
		t.Fatalf("want 2, got %d", got)
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
	b.Emit(Event{Type: "x"})
	mu.Lock()
	defer mu.Unlock()
	if captured.ID == "" {
		t.Fatal("ID should be auto-filled")
	}
	if captured.Timestamp.IsZero() {
		t.Fatal("Timestamp should be auto-filled")
	}
}
