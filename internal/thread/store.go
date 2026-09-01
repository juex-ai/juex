package thread

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
)

const indexFile = "threads.index.json"

type Store struct {
	mu            *sync.Mutex
	agentStateDir string
	random        io.Reader
	now           func() time.Time
}

var storeLocks sync.Map

func NewStore(agentStateDir string) *Store {
	stateDir := filepath.Clean(agentStateDir)
	lock, _ := storeLocks.LoadOrStore(stateDir, &sync.Mutex{})
	return &Store{mu: lock.(*sync.Mutex), agentStateDir: stateDir, random: rand.Reader, now: time.Now}
}

func (s *Store) AgentStateDir() string { return s.agentStateDir }
func (s *Store) ThreadsDir() string    { return filepath.Join(s.agentStateDir, "threads") }
func (s *Store) ArchiveDir() string    { return filepath.Join(s.agentStateDir, "archive", "threads") }
func (s *Store) TrashDir() string      { return filepath.Join(s.agentStateDir, ".trash", "threads") }
func (s *Store) IndexPath() string     { return filepath.Join(s.agentStateDir, indexFile) }

func (s *Store) EnsureMain() (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.ThreadsDir(), MainID)
	if _, err := os.Stat(filepath.Join(dir, journalFile)); err == nil {
		return s.openLocked(dir, MainID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s.createLocked(MainID, MainAlias, "")
}

func (s *Store) CreateWorker(parentID, alias string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(parentID) {
		return nil, fmt.Errorf("%w: parent %q", ErrInvalidID, parentID)
	}
	index, err := s.loadOrRebuildIndexLocked()
	if err != nil {
		return nil, err
	}
	parentFound := false
	for _, entry := range index.Threads {
		if entry.ThreadID == parentID && entry.ArchivedAt == nil {
			parentFound = true
			break
		}
	}
	if !parentFound {
		return nil, fmt.Errorf("thread: active parent %q not found", parentID)
	}
	if alias != "" {
		if err := validateAliasAvailable(index, alias, ""); err != nil {
			return nil, err
		}
	}
	explicitAlias := alias
	for attempt := 0; attempt < 64; attempt++ {
		id, err := newWorkerID(s.random)
		if err != nil {
			return nil, err
		}
		workerAlias := explicitAlias
		if workerAlias == "" {
			workerAlias = DefaultWorkerAlias(id)
		}
		if indexContainsIdentity(index, id) || strings.EqualFold(workerAlias, id) {
			continue
		}
		available, err := s.workerIDAvailableLocked(id)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		if err := validateAliasAvailable(index, workerAlias, ""); err != nil {
			if explicitAlias != "" {
				return nil, err
			}
			continue
		}
		return s.createLocked(id, workerAlias, parentID)
	}
	return nil, fmt.Errorf("thread: worker identity collision limit reached")
}

func indexContainsIdentity(index Index, identity string) bool {
	for _, entry := range index.Threads {
		if strings.EqualFold(entry.ThreadID, identity) || strings.EqualFold(entry.Alias, identity) {
			return true
		}
	}
	return false
}

func (s *Store) workerIDAvailableLocked(id string) (bool, error) {
	for _, root := range []string{s.ThreadsDir(), s.ArchiveDir()} {
		_, err := os.Stat(filepath.Join(root, id))
		switch {
		case err == nil:
			return false, nil
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return false, err
		}
	}
	return true, nil
}

func validateAliasAvailable(index Index, alias, exceptID string) error {
	if alias == "" {
		return fmt.Errorf("thread: alias is required")
	}
	for _, entry := range index.Threads {
		if strings.EqualFold(entry.ThreadID, alias) {
			return fmt.Errorf("thread: alias %q conflicts with Thread ID %s", alias, entry.ThreadID)
		}
		if entry.ThreadID != exceptID && strings.EqualFold(entry.Alias, alias) {
			return fmt.Errorf("thread: alias %q is already used by %s", alias, entry.ThreadID)
		}
	}
	return nil
}

func (s *Store) OpenActive(id string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openLocked(filepath.Join(s.ThreadsDir(), id), id)
}

func (s *Store) OpenArchived(id string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openLocked(filepath.Join(s.ArchiveDir(), id), id)
}

func (s *Store) createLocked(id, alias, parentID string) (*Thread, error) {
	dir := filepath.Join(s.ThreadsDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "scratchpad"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "spool"), 0o700); err != nil {
		return nil, err
	}
	journal, commits, err := openJournal(filepath.Join(dir, journalFile), id, s.now)
	if err != nil {
		return nil, err
	}
	if len(commits) != 0 {
		_ = journal.Close()
		return nil, fmt.Errorf("%w: create target is not empty", ErrCorruptJournal)
	}
	fact := Fact{Type: FactThreadCreated, ThreadID: id, Alias: alias, ParentThreadID: parentID, GenerationID: InitialGeneration}
	commit, start, err := journal.Append(fact)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	lineEnd := journal.size
	state, err := replay(id, []scannedCommit{{Commit: commit, StartOffset: start, EndOffset: lineEnd}})
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	thread := &Thread{ID: id, Dir: dir, journal: journal, state: state, store: s}
	thread.refreshPublicLocked()
	if err := thread.persistProjectionLocked(); err != nil {
		_ = thread.Close()
		return nil, err
	}
	if err := s.updateProjectionLocked(state.Projection); err != nil {
		_ = thread.Close()
		return nil, err
	}
	return thread, nil
}

func (s *Store) openLocked(dir, id string) (*Thread, error) {
	if !ValidID(id) || filepath.Base(dir) != id {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	// openJournal creates its file because creation paths use the same journal
	// primitive. A lookup must prove the durable Thread exists first; otherwise
	// a miss leaves an empty journal that blocks EnsureMain or unarchive.
	if _, err := os.Stat(filepath.Join(dir, journalFile)); err != nil {
		return nil, err
	}
	journal, state, err := openJournalForReplay(filepath.Join(dir, journalFile), id, s.now)
	if err != nil {
		return nil, err
	}
	thread := &Thread{ID: id, Dir: dir, journal: journal, state: state, store: s}
	thread.refreshPublicLocked()
	if err := thread.persistProjectionLocked(); err != nil {
		_ = thread.Close()
		return nil, err
	}
	if err := s.updateProjectionLocked(state.Projection); err != nil {
		_ = thread.Close()
		return nil, err
	}
	return thread, nil
}

func (s *Store) List() ([]IndexEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.loadOrRebuildIndexLocked()
	if err != nil {
		return nil, err
	}
	return append([]IndexEntry(nil), index.Threads...), nil
}

func (s *Store) updateProjectionLocked(projection Projection) error {
	index, err := s.loadOrRebuildIndexLocked()
	if err != nil {
		return err
	}
	entry := indexEntryFromProjection(projection)
	replaced := false
	for i := range index.Threads {
		if index.Threads[i].ThreadID == projection.ThreadID {
			index.Threads[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		index.Threads = append(index.Threads, entry)
	}
	return s.writeIndexLocked(index)
}

func (s *Store) loadOrRebuildIndexLocked() (Index, error) {
	data, err := os.ReadFile(s.IndexPath())
	if err == nil {
		var index Index
		if decodeErr := json.Unmarshal(data, &index); decodeErr == nil && index.Version == ProjectionVersion {
			return index, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Index{}, err
	}
	return s.rebuildIndexLocked()
}

func (s *Store) rebuildIndexLocked() (Index, error) {
	index := Index{Version: ProjectionVersion, UpdatedAt: NewTimestamp(s.now())}
	for _, root := range []string{s.ThreadsDir(), s.ArchiveDir()} {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Index{}, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !ValidID(entry.Name()) {
				continue
			}
			projection, err := s.rebuildProjectionLocked(filepath.Join(root, entry.Name()), entry.Name())
			if err != nil {
				return Index{}, fmt.Errorf("thread: rebuild projection %s: %w", entry.Name(), err)
			}
			index.Threads = append(index.Threads, indexEntryFromProjection(projection))
		}
	}
	if err := s.writeIndexLocked(index); err != nil {
		return Index{}, err
	}
	return index, nil
}

func (s *Store) rebuildProjectionLocked(dir, id string) (projection Projection, resultErr error) {
	journal, state, err := openJournalForReplay(filepath.Join(dir, journalFile), id, s.now)
	if err != nil {
		return Projection{}, err
	}
	target := &Thread{ID: id, Dir: dir, journal: journal, state: state}
	target.refreshPublicLocked()
	if err := target.persistProjectionLocked(); err != nil {
		resultErr = err
	}
	if err := journal.Close(); err != nil {
		resultErr = errors.Join(resultErr, err)
	}
	if resultErr != nil {
		return Projection{}, resultErr
	}
	return cloneProjection(state.Projection), nil
}

func (s *Store) writeIndexLocked(index Index) error {
	index.Version = ProjectionVersion
	index.Revision++
	index.UpdatedAt = NewTimestamp(s.now())
	sort.Slice(index.Threads, func(i, j int) bool {
		left, right := index.Threads[i], index.Threads[j]
		if left.ThreadID == MainID {
			return true
		}
		if right.ThreadID == MainID {
			return false
		}
		if (left.ArchivedAt == nil) != (right.ArchivedAt == nil) {
			return left.ArchivedAt == nil
		}
		if !left.LastActivityAt.Equal(right.LastActivityAt.Time) {
			return left.LastActivityAt.After(right.LastActivityAt.Time)
		}
		return left.ThreadID < right.ThreadID
	})
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return homestore.WriteFileAtomic(s.IndexPath(), data, 0o600, 0o755)
}

func indexEntryFromProjection(projection Projection) IndexEntry {
	entry := IndexEntry{
		ThreadID:            projection.ThreadID,
		Alias:               projection.Alias,
		ParentThreadID:      projection.ParentThreadID,
		ArchivedAt:          projection.ArchivedAt,
		CreatedAt:           projection.CreatedAt,
		LastActivityAt:      projection.LastActivityAt,
		State:               projection.State,
		PendingInputCount:   projection.Counts.PendingInputCount,
		TurnCount:           projection.Counts.TurnCount,
		GenerationCount:     projection.Counts.GenerationCount,
		CurrentGenerationID: projection.CurrentGeneration.ID,
		TokenUsage:          projection.TokenUsage,
		ThreadRevision:      projection.Revision,
	}
	if projection.ContextUsage != nil {
		entry.CurrentContextTokens = projection.ContextUsage.CurrentTokens
	}
	return entry
}
