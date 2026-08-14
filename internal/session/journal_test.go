package session

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestAppendJournalBytesDurablyRollsBackPartialWrite(t *testing.T) {
	path, file := openJournalTestFile(t, []byte("committed\n"))
	want := errors.New("partial write")
	ops := defaultJournalFileOps()
	ops.write = func(file *os.File, data []byte) (int, error) {
		written, err := file.Write(data[:len(data)/2])
		if err != nil {
			return written, err
		}
		return written, want
	}

	err := appendJournalBytesDurably(file, path, int64(len("committed\n")), []byte("uncommitted\n"), ops)
	if !errors.Is(err, want) {
		t.Fatalf("appendJournalBytesDurably() error = %v, want %v", err, want)
	}
	assertJournalBytes(t, path, "committed\n")
}

func TestAppendJournalBytesDurablyRollsBackSyncFailureAndSyncsRollback(t *testing.T) {
	path, file := openJournalTestFile(t, []byte("committed\n"))
	want := errors.New("sync failed")
	ops := defaultJournalFileOps()
	syncCalls := 0
	ops.sync = func(file *os.File) error {
		syncCalls++
		if syncCalls == 1 {
			return want
		}
		return file.Sync()
	}

	err := appendJournalBytesDurably(file, path, int64(len("committed\n")), []byte("uncommitted\n"), ops)
	if !errors.Is(err, want) {
		t.Fatalf("appendJournalBytesDurably() error = %v, want %v", err, want)
	}
	if syncCalls != 2 {
		t.Fatalf("sync calls = %d, want 2", syncCalls)
	}
	assertJournalBytes(t, path, "committed\n")
}

func TestAppendJournalBytesDurablyReportsRollbackFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*journalFileOps, error)
	}{
		{
			name: "truncate",
			edit: func(ops *journalFileOps, want error) {
				ops.truncate = func(string, int64) error { return want }
			},
		},
		{
			name: "sync",
			edit: func(ops *journalFileOps, want error) {
				ops.sync = func(*os.File) error { return want }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, file := openJournalTestFile(t, []byte("committed\n"))
			writeErr := errors.New("write failed")
			rollbackErr := errors.New("rollback failed")
			ops := defaultJournalFileOps()
			ops.write = func(file *os.File, data []byte) (int, error) {
				written, err := file.Write(data)
				if err != nil {
					return written, err
				}
				return written, writeErr
			}
			test.edit(&ops, rollbackErr)

			err := appendJournalBytesDurably(file, path, int64(len("committed\n")), []byte("uncommitted\n"), ops)
			if !errors.Is(err, writeErr) || !errors.Is(err, rollbackErr) {
				t.Fatalf("appendJournalBytesDurably() error = %v, want joined write and rollback errors", err)
			}
		})
	}
}

func TestTranscriptJournalRecordValidation(t *testing.T) {
	message := llm.Message{ID: "message-1", Role: llm.RoleUser, Blocks: []llm.Block{}}
	line, err := marshalTranscriptJournalLine("session-1", 1, message)
	if err != nil {
		t.Fatal(err)
	}
	got, header, err := decodeTranscriptJournalLine(line, journalRecordExpectation{
		kind:      journalKindConversation,
		sessionID: "session-1",
		sequence:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != message.ID || header.Sequence != 1 {
		t.Fatalf("decoded record = (%+v, %+v)", got, header)
	}

	for _, test := range []struct {
		name string
		old  string
		new  string
		want string
	}{
		{name: "version", old: `"journal_version":1`, new: `"journal_version":2`, want: "journal version"},
		{name: "kind", old: `"journal":"conversation"`, new: `"journal":"events"`, want: "journal kind"},
		{name: "session", old: `"session_id":"session-1"`, new: `"session_id":"session-2"`, want: "session identity"},
		{name: "sequence", old: `"sequence":1`, new: `"sequence":3`, want: "journal sequence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := []byte(strings.Replace(string(line), test.old, test.new, 1))
			if _, _, err := decodeTranscriptJournalLine(mutated, journalRecordExpectation{
				kind:      journalKindConversation,
				sessionID: "session-1",
				sequence:  1,
			}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeTranscriptJournalLine() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestEventJournalRecordRoundTrip(t *testing.T) {
	event := events.Event{ID: "event-1", Type: "turn.started", TurnID: "turn-1"}
	line, err := marshalEventJournalLine("session-1", 4, event)
	if err != nil {
		t.Fatal(err)
	}
	got, header, err := decodeEventJournalLine(line, journalRecordExpectation{
		kind:      journalKindEvents,
		sessionID: "session-1",
		sequence:  4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != event.ID || got.Type != event.Type || header.Sequence != 4 {
		t.Fatalf("decoded record = (%+v, %+v)", got, header)
	}
}

func TestTruncateJournalTailDurablySyncsRepair(t *testing.T) {
	path, file := openJournalTestFile(t, []byte("committed\ntorn"))
	ops := defaultJournalFileOps()
	synced := false
	ops.sync = func(file *os.File) error {
		synced = true
		return file.Sync()
	}
	if err := truncateJournalTailDurably(file, path, int64(len("committed\n")), ops); err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("repair did not sync the truncated journal")
	}
	assertJournalBytes(t, path, "committed\n")
}

func TestSessionMetadataRequiresCurrentFormatAndIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-1")
	now := time.Now().UnixMilli()
	if err := saveMetadata(dir, metadata{StartedAtMS: now, LastActiveAtMS: now}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FormatVersion != sessionJournalVersion || loaded.SessionID != "session-1" {
		t.Fatalf("metadata = %+v", loaded)
	}

	path := filepath.Join(dir, metadataFile)
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "version",
			body: `{"format_version":2,"session_id":"session-1","started_at_ms":1,"last_active_at_ms":1}`,
			want: "format version",
		},
		{
			name: "identity",
			body: `{"format_version":1,"session_id":"session-2","started_at_ms":1,"last_active_at_ms":1}`,
			want: "session identity",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.body+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadMetadata(dir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadMetadata() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSessionConversationAppendRollsBackSyncFailure(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	want := errors.New("sync failed")
	syncCalls := 0
	s.journalOps.sync = func(file *os.File) error {
		syncCalls++
		if syncCalls == 1 {
			return want
		}
		return file.Sync()
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "not committed")); !errors.Is(err, want) {
		t.Fatalf("Append() error = %v, want %v", err, want)
	}
	if len(s.History) != 0 || s.transcript.lastSequence != 0 {
		t.Fatalf("resident transcript advanced after failed sync: history=%d sequence=%d", len(s.History), s.transcript.lastSequence)
	}
	assertJournalBytes(t, filepath.Join(s.Dir, conversationFile), "")
}

func TestSessionEventAppendRollsBackSyncFailure(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	want := errors.New("sync failed")
	syncCalls := 0
	s.journalOps.sync = func(file *os.File) error {
		syncCalls++
		if syncCalls == 1 {
			return want
		}
		return file.Sync()
	}
	if err := s.AppendEvent(events.Event{Type: "turn.started"}); !errors.Is(err, want) {
		t.Fatalf("AppendEvent() error = %v, want %v", err, want)
	}
	if s.eventSequence != 0 {
		t.Fatalf("event sequence = %d, want 0", s.eventSequence)
	}
	assertJournalBytes(t, filepath.Join(s.Dir, eventsFile), "")
}

func TestSessionFailedRollbackPoisonsJournal(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("write failed")
	rollbackErr := errors.New("truncate failed")
	writes := 0
	s.journalOps.write = func(file *os.File, data []byte) (int, error) {
		writes++
		written, err := file.Write(data)
		if err != nil {
			return written, err
		}
		return written, writeErr
	}
	s.journalOps.truncate = func(string, int64) error { return rollbackErr }

	first := s.Append(llm.TextMessage(llm.RoleUser, "first"))
	if !errors.Is(first, writeErr) || !errors.Is(first, rollbackErr) {
		t.Fatalf("first Append() error = %v, want write and rollback errors", first)
	}
	second := s.Append(llm.TextMessage(llm.RoleUser, "second"))
	if !errors.Is(second, writeErr) || !errors.Is(second, rollbackErr) {
		t.Fatalf("second Append() error = %v, want stable poisoned-journal error", second)
	}
	if writes != 1 {
		t.Fatalf("journal writes = %d, want 1", writes)
	}
	if closeErr := s.Close(); !errors.Is(closeErr, rollbackErr) {
		t.Fatalf("Close() error = %v, want rollback error", closeErr)
	}
}

func TestSessionCloseSyncsOnceAndReturnsStableError(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("close sync failed")
	syncCalls := 0
	s.journalOps.sync = func(*os.File) error {
		syncCalls++
		return want
	}
	first := s.Close()
	second := s.Close()
	if !errors.Is(first, want) || !errors.Is(second, want) || first.Error() != second.Error() {
		t.Fatalf("Close() errors = (%v, %v), want stable %v", first, second, want)
	}
	if syncCalls != 2 {
		t.Fatalf("sync calls = %d, want one per journal", syncCalls)
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "late")); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Append() after close error = %v, want closed", err)
	}
}

func TestConversationReplayRepairsTornFinalRecord(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "committed")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, conversationFile)
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"journal_version":1`); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	loaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if len(loaded.History) != 1 || loaded.History[0].FirstText() != "committed" {
		t.Fatalf("history after repair = %+v", loaded.History)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(committed) {
		t.Fatalf("repaired transcript = %q, want %q", got, committed)
	}
}

func TestEventReplayRepairsTornFinalRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line, err := marshalEventJournalLine("session-1", 1, events.Event{ID: "e1", Type: "turn.started"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, eventsFile)
	contents := append(append([]byte(nil), line...), []byte(`{"journal_version":1`)...)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "e1" {
		t.Fatalf("events after repair = %+v", got)
	}
	assertJournalBytes(t, path, string(line))
}

func TestCompleteJournalCorruptionAndSequenceGapAreHardFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		tail func(string) []byte
		want string
	}{
		{name: "corrupt complete record", tail: func(string) []byte { return []byte("not-json\n") }, want: "invalid character"},
		{name: "sequence gap", tail: func(id string) []byte {
			line, err := marshalEventJournalLine(id, 3, events.Event{ID: "e3", Type: "turn.completed"})
			if err != nil {
				panic(err)
			}
			return line
		}, want: "sequence 3, want 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session-1")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			line, err := marshalEventJournalLine("session-1", 1, events.Event{ID: "e1", Type: "turn.started"})
			if err != nil {
				t.Fatal(err)
			}
			contents := append(append([]byte(nil), line...), test.tail("session-1")...)
			path := filepath.Join(dir, eventsFile)
			if err := os.WriteFile(path, contents, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadEvents(dir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadEvents() error = %v, want containing %q", err, test.want)
			}
			assertJournalBytes(t, path, string(contents))
		})
	}
}

func openJournalTestFile(t *testing.T, initial []byte) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	return path, file
}

func assertJournalBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("journal bytes = %q, want %q", got, want)
	}
}

func mustTranscriptLine(t *testing.T, sessionID string, sequence uint64, message llm.Message) []byte {
	t.Helper()
	line, err := marshalTranscriptJournalLine(sessionID, sequence, message)
	if err != nil {
		t.Fatal(err)
	}
	return line
}
