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
	if err := os.WriteFile(filepath.Join(dir, eventsFile), []byte(
		"{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}\nnot-json\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(dir)
	if err == nil {
		t.Fatal("ReadEvents() error = nil")
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("partial events = %+v, want first valid event", got)
	}
}

func TestReadEventsRepairsMalformedTailBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	if err := os.WriteFile(path, []byte(
		"{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}\n"+
			"{\"id\":\"partial\"",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	appendFD, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer appendFD.Close()

	got, err := ReadEvents(dir)
	if err == nil {
		t.Fatal("ReadEvents() error = nil")
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("partial events = %+v, want first valid event", got)
	}
	if err := writeJSONL(appendFD, events.Event{
		ID:     "2",
		Type:   "turn.completed",
		TurnID: "turn-1",
	}); err != nil {
		t.Fatal(err)
	}

	got, err = ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() after append error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("events after repair and append = %+v, want ids 1 and 2", got)
	}
}

func TestReadEventsTerminatesValidTailBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	if err := os.WriteFile(path, []byte(
		"{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	appendFD, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer appendFD.Close()

	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("events = %+v, want first valid event", got)
	}
	if err := writeJSONL(appendFD, events.Event{
		ID:     "2",
		Type:   "turn.completed",
		TurnID: "turn-1",
	}); err != nil {
		t.Fatal(err)
	}

	got, err = ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() after append error = %v", err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("events after terminate and append = %+v, want ids 1 and 2", got)
	}
}
