package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

func FuzzJournalRepairsArbitraryTornTail(f *testing.F) {
	f.Add([]byte(`{"v":1,"seq":2`))
	f.Add([]byte{0, 1, 2, 0xff})
	f.Fuzz(func(t *testing.T, tail []byte) {
		tail = bytes.ReplaceAll(tail, []byte{'\n'}, []byte{'x'})
		if len(tail) == 0 {
			tail = []byte{'{'}
		}
		dir := filepath.Join(t.TempDir(), MainID)
		path := filepath.Join(dir, journalFile)
		journal, _, err := openJournal(path, MainID, fixedNow())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := journal.Append(mainCreatedFact()); err != nil {
			t.Fatal(err)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(tail); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, commits, err := openJournal(path, MainID, fixedNow())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reopened.Close() }()
		if len(commits) != 1 || commits[0].Seq != 1 {
			t.Fatalf("commits = %#v", commits)
		}
	})
}

func TestJournalRepairsOnlyTornFinalLine(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), MainID)
	path := filepath.Join(dir, journalFile)
	now := fixedNow()
	journal, _, err := openJournal(path, MainID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Append(mainCreatedFact()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	validSize := info.Size()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"v":1,"seq":2`); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reopened, commits, err := openJournal(path, MainID, now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if len(commits) != 1 || commits[0].Seq != 1 {
		t.Fatalf("commits = %#v", commits)
	}
	repaired, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Size() != validSize {
		t.Fatalf("repaired size = %d, want %d", repaired.Size(), validSize)
	}
}

func TestJournalRejectsMalformedCompleteCommit(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), MainID)
	path := filepath.Join(dir, journalFile)
	journal, _, err := openJournal(path, MainID, fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Append(mainCreatedFact()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("not-json\n")
	_ = file.Close()
	if _, _, err := openJournal(path, MainID, fixedNow()); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("error = %v, want ErrCorruptJournal", err)
	}
}

func TestJournalRollsBackPartialWrite(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), MainID)
	path := filepath.Join(dir, journalFile)
	journal, _, err := openJournal(path, MainID, fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if _, _, err := journal.Append(mainCreatedFact()); err != nil {
		t.Fatal(err)
	}
	wantSize := journal.size
	injected := errors.New("injected partial write")
	wrote := false
	journal.ops.write = func(file *os.File, data []byte) (int, error) {
		if wrote {
			return 0, injected
		}
		wrote = true
		n := len(data) / 2
		written, err := file.Write(data[:n])
		if err != nil {
			return written, err
		}
		return written, injected
	}
	event := events.Event{ID: "evt_1", Type: "test.event", Timestamp: time.Date(2026, 9, 1, 0, 0, 0, 123000000, time.UTC)}
	if _, _, err := journal.Append(Fact{Type: FactEventRecorded, Event: &event}); !errors.Is(err, injected) {
		t.Fatalf("error = %v, want injected", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != wantSize || journal.size != wantSize || journal.nextSeq != 2 {
		t.Fatalf("size=%d resident=%d next=%d", info.Size(), journal.size, journal.nextSeq)
	}
}

func fixedNow() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 9, 1, 0, 0, 0, 123000000, time.UTC)
	}
}

func mainCreatedFact() Fact {
	return Fact{Type: FactThreadCreated, ThreadID: MainID, Alias: MainAlias, GenerationID: InitialGeneration}
}
