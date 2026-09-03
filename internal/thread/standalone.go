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
	eventStore, created, err := createEventStore(dir, id, alias, parentID, now)
	if err != nil {
		return nil, cleanupCreatedThreadDir(dir, nil, err)
	}
	state, err := replay(id, []scannedCommit{created})
	if err != nil {
		return nil, cleanupCreatedThreadDir(dir, eventStore, err)
	}
	state.Projection.Revision = 1
	target := &Thread{ID: id, Dir: dir, eventStore: eventStore, state: state}
	target.refreshPublicLocked()
	if err := target.persistProjectionLocked(); err != nil {
		return nil, cleanupCreatedThreadDir(dir, eventStore, err)
	}
	return target, nil
}

func Load(dir string) (*Thread, error) {
	id := filepath.Base(filepath.Clean(dir))
	metadata, err := readProjectionFile(dir, id)
	if err != nil {
		return nil, err
	}
	eventStore, state, err := openEventStore(dir, id, metadata, time.Now)
	if err != nil {
		return nil, err
	}
	if err := recoverContextRenewalFiles(dir, metadata.CurrentGeneration.ID); err != nil {
		_ = eventStore.Close()
		return nil, fmt.Errorf("thread: recover Context renewal files for %s: %w", id, err)
	}
	target := &Thread{ID: id, Dir: dir, eventStore: eventStore, state: state}
	target.refreshPublicLocked()
	return target, nil
}

func LoadInfo(dir string) (Info, []llm.Message, error) {
	id := filepath.Base(filepath.Clean(dir))
	metadata, err := readProjectionFile(dir, id)
	if err != nil {
		return Info{}, nil, err
	}
	eventStore, state, err := openEventStore(dir, id, metadata, time.Now)
	if err != nil {
		return Info{}, nil, err
	}
	if err := recoverContextRenewalFiles(dir, metadata.CurrentGeneration.ID); err != nil {
		_ = eventStore.Close()
		return Info{}, nil, fmt.Errorf("thread: recover Context renewal files for %s: %w", id, err)
	}
	target := &Thread{ID: id, Dir: dir, eventStore: eventStore, state: state}
	target.refreshPublicLocked()
	info := target.infoLocked()
	messages := append([]llm.Message(nil), target.state.Messages...)
	if err := eventStore.Close(); err != nil {
		return Info{}, nil, err
	}
	return info, messages, nil
}
