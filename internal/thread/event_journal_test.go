package thread

import (
	"testing"

	"github.com/juex-ai/juex/internal/events"
)

func TestEventStoreSnapshotIncludesEveryGenerationAndExcludesLaterCommits(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []events.Event{
		{ID: "before-checkpoint", Type: "turn.started", TurnID: "turn-1"},
		{ID: "checkpoint-terminal", Type: "turn.completed", TurnID: "turn-1"},
	} {
		if err := main.AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := main.AppendEvent(events.Event{ID: "after-checkpoint", Type: "turn.started", TurnID: "turn-2"}); err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if currentEvents := reopened.ReplaySnapshot().Events; len(currentEvents) != 2 {
		t.Fatalf("current Generation retained %d recovery events, want 2", len(currentEvents))
	}

	snapshot, err := reopened.CaptureEventStore()
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

func TestEventStoreSnapshotSurvivesThreadDirectoryMove(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.CreateWorker(main.ID, "snapshot-move")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.AppendEvent(events.Event{ID: "before-move", Type: "turn.started", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	if err := worker.AppendEvent(events.Event{ID: "completed-before-move", Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := worker.CaptureEventStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()

	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	got, err := snapshot.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "before-move" || got[1].ID != "completed-before-move" {
		t.Fatalf("captured events after move = %#v", got)
	}
	journals, err := snapshot.GenerationJournals()
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 || len(journals[0].Data) == 0 {
		t.Fatalf("captured journals after move = %#v", journals)
	}
}
