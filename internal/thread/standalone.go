package thread

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

// New creates an isolated Worker Thread below root. Production Agent runtimes
// should use Store so Main uniqueness and the Agent-level index are enforced.
func New(root string) (*Thread, error) {
	for attempt := 0; attempt < 64; attempt++ {
		id, err := newWorkerID(rand.Reader)
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(root, id)
		if _, err := os.Stat(dir); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return createStandalone(dir, id, DefaultWorkerAlias(id), MainID, time.Now)
	}
	return nil, fmt.Errorf("thread: standalone id collision limit reached")
}

func createStandalone(dir, id, alias, parentID string, now func() time.Time) (*Thread, error) {
	if err := os.MkdirAll(filepath.Join(dir, "scratchpad"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "spool"), 0o700); err != nil {
		return nil, err
	}
	journal, commits, err := openJournal(filepath.Join(dir, journalFile), id, now)
	if err != nil {
		return nil, err
	}
	if len(commits) != 0 {
		_ = journal.Close()
		return nil, fmt.Errorf("%w: create target is not empty", ErrCorruptJournal)
	}
	commit, start, err := journal.Append(Fact{
		Type: FactThreadCreated, ThreadID: id, Alias: alias,
		ParentThreadID: parentID, GenerationID: InitialGeneration,
	})
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	state, err := replay(id, []scannedCommit{{Commit: commit, StartOffset: start, EndOffset: journal.size}})
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	target := &Thread{ID: id, Dir: dir, journal: journal, state: state}
	target.refreshPublicLocked()
	if err := target.persistProjectionLocked(); err != nil {
		_ = target.Close()
		return nil, err
	}
	return target, nil
}

func Load(dir string) (*Thread, error) {
	id := filepath.Base(filepath.Clean(dir))
	if _, err := os.Stat(filepath.Join(dir, journalFile)); err != nil {
		return nil, err
	}
	journal, state, err := openJournalForReplay(filepath.Join(dir, journalFile), id, time.Now)
	if err != nil {
		return nil, err
	}
	target := &Thread{ID: id, Dir: dir, journal: journal, state: state}
	target.refreshPublicLocked()
	if err := target.persistProjectionLocked(); err != nil {
		_ = target.Close()
		return nil, err
	}
	return target, nil
}

func LoadInfo(dir string) (Info, []llm.Message, error) {
	id := filepath.Base(filepath.Clean(dir))
	if _, err := os.Stat(filepath.Join(dir, journalFile)); err != nil {
		return Info{}, nil, err
	}
	journal, commits, err := openJournal(filepath.Join(dir, journalFile), id, time.Now)
	if err != nil {
		return Info{}, nil, err
	}
	defer journal.Close()
	state, err := replay(id, commits)
	if err != nil {
		return Info{}, nil, err
	}
	target := &Thread{ID: id, Dir: dir, state: state}
	target.refreshPublicLocked()
	return target.infoLocked(), append([]llm.Message(nil), target.state.Messages...), nil
}
