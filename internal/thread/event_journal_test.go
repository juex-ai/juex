package thread

import (
	"testing"

	"github.com/juex-ai/juex/internal/events"
)

func TestEventJournalSnapshotIncludesEventsBeforeCheckpointAfterReopen(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []events.Event{
		{ID: "before-checkpoint", Type: "turn.started", TurnID: "turn-1"},
		{ID: "checkpoint-terminal", Type: "turn.completed", TurnID: "turn-1"},
		{ID: "after-checkpoint", Type: "turn.started", TurnID: "turn-2"},
	} {
		if err := main.AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if eventsAfterCheckpoint := reopened.ReplaySnapshot().Events; len(eventsAfterCheckpoint) != 2 {
		t.Fatalf("checkpoint projection retained %d events, want 2", len(eventsAfterCheckpoint))
	}

	snapshot, err := reopened.CaptureEventJournal()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := reopened.AppendEvent(events.Event{ID: "after-snapshot", Type: "turn.started", TurnID: "turn-3"}); err != nil {
		t.Fatal(err)
	}
	got, err := snapshot.Events()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"before-checkpoint", "checkpoint-terminal", "after-checkpoint"}
	if len(got) != len(want) {
		t.Fatalf("captured events = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].ID != want[index] {
			t.Fatalf("captured event %d = %q, want %q", index, got[index].ID, want[index])
		}
	}
}
