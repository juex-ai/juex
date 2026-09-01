package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/homestore"
)

func (s *Store) Archive(target *Thread) error {
	if target == nil || target.ID == MainID {
		return fmt.Errorf("thread: Main cannot be archived")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	projection := target.Projection()
	if projection.State == StateWorking || projection.Counts.PendingInputCount != 0 {
		return fmt.Errorf("thread: archive %s: Thread is busy", target.ID)
	}
	if _, err := target.appendFactsStoreLocked(Fact{Type: FactThreadArchived}); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	source := filepath.Join(s.ThreadsDir(), target.ID)
	destination := filepath.Join(s.ArchiveDir(), target.ID)
	if err := durableRename(source, destination); err != nil {
		return fmt.Errorf("thread: archive %s: %w", target.ID, err)
	}
	target.Dir = destination
	return nil
}

func (s *Store) Unarchive(id string) (*Thread, error) {
	if !ValidWorkerID(id) {
		return nil, fmt.Errorf("%w: archived id %q", ErrInvalidID, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, err := s.openLocked(filepath.Join(s.ArchiveDir(), id), id)
	if err != nil {
		return nil, err
	}
	if _, err := target.appendFactsStoreLocked(Fact{Type: FactThreadUnarchived}); err != nil {
		_ = target.Close()
		return nil, err
	}
	if err := target.Close(); err != nil {
		return nil, err
	}
	source := filepath.Join(s.ArchiveDir(), id)
	destination := filepath.Join(s.ThreadsDir(), id)
	if err := durableRename(source, destination); err != nil {
		return nil, fmt.Errorf("thread: unarchive %s: %w", id, err)
	}
	return s.openLocked(destination, id)
}

func (s *Store) DeleteArchived(id string) error {
	if !ValidWorkerID(id) {
		return fmt.Errorf("%w: delete id %q", ErrInvalidID, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.loadOrRebuildIndexLocked()
	if err != nil {
		return err
	}
	found := false
	for _, entry := range index.Threads {
		if entry.ParentThreadID == id {
			return fmt.Errorf("thread: delete %s: child %s still references it", id, entry.ThreadID)
		}
		if entry.ThreadID == id {
			if entry.ArchivedAt == nil {
				return fmt.Errorf("thread: delete %s: Thread is active", id)
			}
			found = true
		}
	}
	if !found {
		return os.ErrNotExist
	}
	source := filepath.Join(s.ArchiveDir(), id)
	trash := filepath.Join(s.TrashDir(), id+"."+newRecordID("delete_"))
	if err := durableRename(source, trash); err != nil {
		return err
	}
	filtered := index.Threads[:0]
	for _, entry := range index.Threads {
		if entry.ThreadID != id {
			filtered = append(filtered, entry)
		}
	}
	index.Threads = filtered
	if err := s.writeIndexLocked(index); err != nil {
		rollbackErr := durableRename(trash, source)
		return errors.Join(err, rollbackErr)
	}
	if err := os.RemoveAll(trash); err != nil {
		return fmt.Errorf("thread: remove trash %s: %w", trash, err)
	}
	return homestore.SyncDir(filepath.Dir(trash))
}

func (s *Store) RecoverLayout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, root := range []string{s.ThreadsDir(), s.ArchiveDir()} {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !ValidWorkerID(entry.Name()) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			data, err := os.ReadFile(filepath.Join(path, projectionFile))
			if err != nil {
				continue
			}
			var projection Projection
			if err := json.Unmarshal(data, &projection); err != nil {
				continue
			}
			archivedNamespace := root == s.ArchiveDir()
			shouldArchive := projection.ArchivedAt != nil
			if shouldArchive == archivedNamespace {
				continue
			}
			destinationRoot := s.ThreadsDir()
			if shouldArchive {
				destinationRoot = s.ArchiveDir()
			}
			if err := durableRename(path, filepath.Join(destinationRoot, entry.Name())); err != nil {
				return err
			}
		}
	}
	if _, err := s.rebuildIndexLocked(); err != nil {
		return err
	}
	return s.finishTrashOperationsLocked()
}

func (s *Store) finishTrashOperationsLocked() error {
	entries, err := os.ReadDir(s.TrashDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		threadID, _, ok := strings.Cut(entry.Name(), ".")
		if !entry.IsDir() || !ok || !ValidWorkerID(threadID) {
			return fmt.Errorf("thread: unknown trash operation %q", entry.Name())
		}
		if err := os.RemoveAll(filepath.Join(s.TrashDir(), entry.Name())); err != nil {
			return fmt.Errorf("thread: finish trash operation %s: %w", entry.Name(), err)
		}
	}
	return homestore.SyncDir(s.TrashDir())
}

func durableRename(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return errors.Join(
		homestore.SyncDir(filepath.Dir(source)),
		homestore.SyncDir(filepath.Dir(destination)),
	)
}
