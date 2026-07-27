package session

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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

func TestReplayEventsMatchesReadEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFile), []byte(
		"{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}\n"+
			"{\"id\":\"2\",\"type\":\"turn.completed\",\"turn_id\":\"turn-1\"}\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	want, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []events.Event
	if err := ReplayEvents(dir, func(event events.Event) {
		got = append(got, event)
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed events = %#v, want %#v", got, want)
	}
}

func TestReplayEventsRepairsOversizedTailAfterValidPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	valid := []byte("{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}\n")
	oversized := []byte(strings.Repeat("x", maxEventLineBytes+1))
	if err := os.WriteFile(path, append(valid, oversized...), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []events.Event
	err := ReplayEvents(dir, func(event events.Event) {
		got = append(got, event)
	})
	if err == nil || !strings.Contains(err.Error(), errEventLineTooLong.Error()) ||
		!strings.Contains(err.Error(), "repaired corrupt tail") {
		t.Fatalf("ReplayEvents() error = %v, want repaired oversized-tail error", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("partial events = %+v, want first valid event", got)
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repaired, valid) {
		t.Fatalf("repaired journal bytes = %d, want valid prefix bytes = %d", len(repaired), len(valid))
	}
}

func TestReplayEventsReadOnlyMalformedTailPreservesBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only file mode fallback is not portable to Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	contents := []byte(
		"{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}\n" +
			"not-json\n",
	)
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Errorf("restore journal mode: %v", err)
		}
	})
	requireReadOnlyJournal(t, path)

	var got []events.Event
	err := ReplayEvents(dir, func(event events.Event) {
		got = append(got, event)
	})
	if err == nil || strings.Contains(err.Error(), "repaired corrupt tail") {
		t.Fatalf("ReplayEvents() error = %v, want unrepaired decode error", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("partial events = %+v, want first valid event", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, contents) {
		t.Fatalf("read-only journal changed:\ngot  %q\nwant %q", after, contents)
	}
}

func TestReplayEventsReadOnlyValidTailWithoutNewlineSucceeds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only file mode fallback is not portable to Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	contents := []byte("{\"id\":\"1\",\"type\":\"turn.started\",\"turn_id\":\"turn-1\"}")
	if err := os.WriteFile(path, contents, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Errorf("restore journal mode: %v", err)
		}
	})
	requireReadOnlyJournal(t, path)

	var got []events.Event
	if err := ReplayEvents(dir, func(event events.Event) {
		got = append(got, event)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("events = %+v, want first valid event", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, contents) {
		t.Fatalf("read-only valid journal changed:\ngot  %q\nwant %q", after, contents)
	}
}

func requireReadOnlyJournal(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Skip("process privileges bypass read-only file mode")
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
