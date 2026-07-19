package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/events"
)

func TestReadEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFile), []byte(
		"{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}\n"+
			"{\"id\":\"2\",\"type\":\"turn.completed\",\"turn_id\":\"turn-1\"}\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []events.Event{
		{ID: "1", Type: "turn.started", TurnID: "turn-1"},
		{ID: "2", Type: "turn.completed", TurnID: "turn-1"},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Type != want[i].Type || got[i].TurnID != want[i].TurnID {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadEventsMissingJournal(t *testing.T) {
	got, err := ReadEvents(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("events = %#v, want nil", got)
	}
}

func TestReadEventsRejectsMalformedJournal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFile), []byte("{}\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(dir); err == nil {
		t.Fatal("ReadEvents() error = nil")
	}
}
