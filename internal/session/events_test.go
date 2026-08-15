package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

func TestReadEvents(t *testing.T) {
	dir := t.TempDir()
	want := []events.Event{
		{ID: "1", Type: "turn.started", TurnID: "turn-1"},
		{ID: "2", Type: "turn.completed", TurnID: "turn-1"},
	}
	if err := os.WriteFile(filepath.Join(dir, eventsFile), eventJournalBytes(t, filepath.Base(dir), want), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
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
	eventsOnDisk := []events.Event{
		{ID: "1", Type: "turn.started", TurnID: "turn-1"},
		{ID: "2", Type: "turn.completed", TurnID: "turn-1"},
	}
	if err := os.WriteFile(filepath.Join(dir, eventsFile), eventJournalBytes(t, filepath.Base(dir), eventsOnDisk), 0o600); err != nil {
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
	valid := eventJournalBytes(t, filepath.Base(dir), []events.Event{{ID: "1", Type: "turn.started", TurnID: "turn-1"}})
	oversized := []byte(strings.Repeat("x", maxEventLineBytes+1))
	if err := os.WriteFile(path, append(valid, oversized...), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []events.Event
	err := ReplayEvents(dir, func(event events.Event) {
		got = append(got, event)
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v, want repaired oversized tail", err)
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
		string(eventJournalBytes(t, filepath.Base(dir), []events.Event{{ID: "1", Type: "turn.started", TurnID: "turn-1"}})) + "not-json\n",
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

func TestReplayEventsReadOnlyTornTailFailsWithoutPublishing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only file mode fallback is not portable to Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	contents := eventJournalBytes(t, filepath.Base(dir), []events.Event{{ID: "1", Type: "turn.started", TurnID: "turn-1"}})
	contents = contents[:len(contents)-1]
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
	if err == nil || !strings.Contains(err.Error(), "torn journal tail") {
		t.Fatalf("ReplayEvents() error = %v, want torn journal tail", err)
	}
	if len(got) != 0 {
		t.Fatalf("events = %+v, want none from incomplete record", got)
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

func TestReadLatestCommittedEventID(t *testing.T) {
	dir := newEventSessionDir(t, "latest")
	want := []events.Event{
		{ID: "evt-first", Type: "turn.started", TurnID: "turn-1"},
		{ID: "evt-latest", Type: "turn.completed", TurnID: "turn-1"},
	}
	if err := os.WriteFile(filepath.Join(dir, eventsFile), eventJournalBytes(t, filepath.Base(dir), want), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLatestCommittedEventID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "evt-latest" {
		t.Fatalf("ReadLatestCommittedEventID() = %q, want evt-latest", got)
	}
}

func TestReadLatestCommittedEventIDMissingOrEmptyJournal(t *testing.T) {
	for _, setup := range []struct {
		name string
		run  func(*testing.T, string)
	}{
		{name: "missing", run: func(*testing.T, string) {}},
		{name: "empty", run: func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, eventsFile), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(setup.name, func(t *testing.T) {
			dir := newEventSessionDir(t, setup.name)
			setup.run(t, dir)
			got, err := ReadLatestCommittedEventID(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != "" {
				t.Fatalf("ReadLatestCommittedEventID() = %q, want empty", got)
			}
			if setup.name == "missing" {
				if _, err := os.Stat(sessionLockGuardPath(dir)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing journal guard stat error = %v, want not exist", err)
				}
			}
		})
	}
}

func TestReadLatestCommittedEventIDIgnoresTornTailWithoutRepairingJournal(t *testing.T) {
	dir := newEventSessionDir(t, "torn-tail")
	path := filepath.Join(dir, eventsFile)
	committed := eventJournalBytes(t, filepath.Base(dir), []events.Event{{
		ID: "evt-committed", Type: "turn.started", TurnID: "turn-1",
	}})
	contents := append(append([]byte(nil), committed...), []byte(`{"journal_version":1`)...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLatestCommittedEventID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "evt-committed" {
		t.Fatalf("ReadLatestCommittedEventID() = %q, want evt-committed", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, contents) {
		t.Fatalf("ReadLatestCommittedEventID() changed the journal:\ngot  %q\nwant %q", after, contents)
	}
}

func TestReadLatestCommittedEventIDRejectsMalformedCompleteTail(t *testing.T) {
	dir := newEventSessionDir(t, "malformed-tail")
	path := filepath.Join(dir, eventsFile)
	committed := eventJournalBytes(t, filepath.Base(dir), []events.Event{{
		ID: "evt-committed", Type: "turn.started", TurnID: "turn-1",
	}})
	if err := os.WriteFile(path, append(committed, []byte("not-json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadLatestCommittedEventID(dir); err == nil {
		t.Fatal("ReadLatestCommittedEventID() error = nil, want malformed complete tail error")
	}
}

func TestReadLatestCommittedEventIDRejectsCompleteEmptyRecord(t *testing.T) {
	for _, suffix := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("suffix_%q", suffix), func(t *testing.T) {
			dir := newEventSessionDir(t, "empty-record")
			committed := eventJournalBytes(t, filepath.Base(dir), []events.Event{{
				ID: "evt-committed", Type: "turn.started", TurnID: "turn-1",
			}})
			if err := os.WriteFile(filepath.Join(dir, eventsFile), append(committed, suffix...), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadLatestCommittedEventID(dir); err == nil {
				t.Fatal("ReadLatestCommittedEventID() error = nil, want empty complete record error")
			}
		})
	}
}

func TestReadLatestCommittedEventIDWaitsForEventSync(t *testing.T) {
	sess, err := New(filepath.Join(t.TempDir(), "agent", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	syncCalls := 0
	sess.journalOps.sync = func(file *os.File) error {
		syncCalls++
		if syncCalls == 1 {
			close(syncStarted)
			<-releaseSync
		}
		return file.Sync()
	}
	appendDone := make(chan error, 1)
	go func() {
		appendDone <- sess.AppendEvent(events.Event{ID: "evt-after-sync", Type: "turn.started", TurnID: "turn-1"})
	}()
	<-syncStarted

	type result struct {
		id  string
		err error
	}
	readDone := make(chan result, 1)
	go func() {
		id, err := ReadLatestCommittedEventID(sess.Dir)
		readDone <- result{id: id, err: err}
	}()
	select {
	case got := <-readDone:
		t.Fatalf("cursor read completed before event Sync: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseSync)
	if err := <-appendDone; err != nil {
		t.Fatal(err)
	}
	got := <-readDone
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.id != "evt-after-sync" {
		t.Fatalf("cursor after Sync = %q, want evt-after-sync", got.id)
	}
}

func TestReadLatestCommittedEventIDDoesNotRecreateDeletedSession(t *testing.T) {
	dir := newEventSessionDir(t, "delete-race")
	if err := os.WriteFile(filepath.Join(dir, eventsFile), eventJournalBytes(t, filepath.Base(dir), []events.Event{{
		ID: "evt-before-delete", Type: "turn.started", TurnID: "turn-1",
	}}), 0o600); err != nil {
		t.Fatal(err)
	}
	deleteLock, err := AcquireSessionDeleteLock(dir, "delete")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		id  string
		err error
	}
	readDone := make(chan result, 1)
	go func() {
		id, err := ReadLatestCommittedEventID(dir)
		readDone <- result{id: id, err: err}
	}()
	select {
	case got := <-readDone:
		t.Fatalf("cursor read completed before delete lock released: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := deleteLock.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-readDone
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.id != "" {
		t.Fatalf("cursor after delete = %q, want empty", got.id)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted Session directory stat error = %v, want not exist", err)
	}
}

func newEventSessionDir(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadEventsAcceptsWorstCaseEscapedTerminalContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	content := strings.Repeat("<", 1<<20)
	eventsOnDisk := []events.Event{{
		ID:     "1",
		Type:   "tool.completed",
		TurnID: "turn-1",
		Payload: map[string]any{
			"name":        "exec_command",
			"tool_use_id": "call-1",
			"content":     content,
		},
	}, {ID: "2", Type: "turn.completed", TurnID: "turn-1"}}
	if err := os.WriteFile(path, eventJournalBytes(t, filepath.Base(dir), eventsOnDisk), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 4<<20 || info.Size() >= int64(maxEventLineBytes) {
		t.Fatalf("encoded journal size = %d, want between old and current line limits", info.Size())
	}

	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("events = %+v, want terminal event followed by completion", got)
	}
	payload, ok := got[0].Payload.(map[string]any)
	if !ok || payload["content"] != content {
		t.Fatalf("terminal content did not round trip")
	}
}

func TestReadEventsRejectsMalformedJournal(t *testing.T) {
	dir := t.TempDir()
	valid := eventJournalBytes(t, filepath.Base(dir), []events.Event{{ID: "1", Type: "turn.started", TurnID: "turn-1"}})
	if err := os.WriteFile(filepath.Join(dir, eventsFile), append(valid, []byte("not-json\n")...), 0o600); err != nil {
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
	valid := eventJournalBytes(t, filepath.Base(dir), []events.Event{{ID: "1", Type: "turn.started", TurnID: "turn-1"}})
	if err := os.WriteFile(path, append(valid, []byte(`{"journal_version":1`)...), 0o600); err != nil {
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
		t.Fatalf("partial events = %+v, want first valid event", got)
	}
	next, err := marshalEventJournalLine(filepath.Base(dir), 2, events.Event{
		ID:     "2",
		Type:   "turn.completed",
		TurnID: "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendFD.Write(next); err != nil {
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

func TestReadEventsDiscardsCompleteJSONWithoutCommitNewlineBeforeAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	torn := eventJournalBytes(t, filepath.Base(dir), []events.Event{{ID: "1", Type: "turn.started", TurnID: "turn-1"}})
	torn = torn[:len(torn)-1]
	if err := os.WriteFile(path, torn, 0o600); err != nil {
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
	if len(got) != 0 {
		t.Fatalf("events = %+v, want torn record discarded", got)
	}
	next, err := marshalEventJournalLine(filepath.Base(dir), 1, events.Event{
		ID:     "2",
		Type:   "turn.completed",
		TurnID: "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendFD.Write(next); err != nil {
		t.Fatal(err)
	}

	got, err = ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() after append error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("events after repair and append = %+v, want only id 2", got)
	}
}

func eventJournalBytes(t *testing.T, sessionID string, journalEvents []events.Event) []byte {
	t.Helper()
	var data []byte
	for i, event := range journalEvents {
		line, err := marshalEventJournalLine(sessionID, uint64(i+1), event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
	}
	return data
}
