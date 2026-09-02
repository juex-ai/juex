package thread

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
)

const indexFile = "threads.index.json"

type Store struct {
	mu              *sync.Mutex
	agentStateDir   string
	random          io.Reader
	now             func() time.Time
	writeIndex      func(string, []byte) error
	writeProjection func(string, []byte) error
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
	if _, err := os.Stat(filepath.Join(dir, projectionFile)); err == nil {
		return s.openLocked(dir, MainID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("%w for %s: metadata is missing", ErrInvalidMetadata, MainID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s.createLocked(MainID, MainAlias, "")
}

func (s *Store) CreateWorker(parentID, alias string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alias = strings.TrimSpace(alias)
	if !ValidID(parentID) {
		return nil, fmt.Errorf("%w: parent %q", ErrInvalidID, parentID)
	}
	index, err := s.loadOrRebuildIndexLocked()
	if err != nil {
		return nil, err
	}
	parentFound := false
	for _, entry := range index.Threads {
		if entry.ThreadID == parentID && entry.RetentionState == RetentionActive {
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
	if strings.HasPrefix(alias, "#") {
		return fmt.Errorf("thread: alias %q conflicts with the #<thread-id> selector", alias)
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
	threadsDir := s.ThreadsDir()
	_, statErr := os.Stat(threadsDir)
	threadsDirCreated := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !threadsDirCreated {
		return nil, statErr
	}
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(threadsDir, "."+id+".creating-")
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, err
	}
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
	state.Projection.Revision = 1
	thread := &Thread{ID: id, Dir: dir, journal: journal, state: state, store: s}
	thread.refreshPublicLocked()
	if err := s.persistInitialProjectionLocked(thread); err != nil {
		_ = thread.Close()
		return nil, err
	}
	if err := thread.Close(); err != nil {
		return nil, err
	}
	destination := filepath.Join(threadsDir, id)
	if _, err := os.Stat(destination); err == nil {
		return nil, fmt.Errorf("thread: create target %s already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(dir, destination); err != nil {
		return nil, err
	}
	published = true
	if err := homestore.SyncDir(threadsDir); err != nil {
		return nil, err
	}
	if threadsDirCreated {
		if err := homestore.SyncDir(filepath.Dir(threadsDir)); err != nil {
			return nil, err
		}
	}
	if err := s.updateProjectionLocked(); err != nil {
		return nil, err
	}
	return s.openLocked(destination, id)
}

func (s *Store) persistInitialProjectionLocked(thread *Thread) error {
	if s.writeProjection == nil {
		return thread.persistProjectionLocked()
	}
	data, err := thread.projectionDataLocked()
	if err != nil {
		return err
	}
	return s.writeProjection(filepath.Join(thread.Dir, projectionFile), data)
}

func (s *Store) openLocked(dir, id string) (*Thread, error) {
	if !ValidID(id) || filepath.Base(dir) != id {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	metadata, err := readProjectionFile(dir, id)
	if err != nil {
		return nil, err
	}
	archivedNamespace := filepath.Clean(filepath.Dir(dir)) == filepath.Clean(s.ArchiveDir())
	if (metadata.RetentionState == RetentionArchived) != archivedNamespace {
		return nil, fmt.Errorf("%w for %s: retention state does not match directory namespace", ErrInvalidMetadata, id)
	}
	if _, err := os.Stat(filepath.Join(dir, journalFile)); err != nil {
		return nil, err
	}
	journal, state, err := openJournalForReplay(filepath.Join(dir, journalFile), id, s.now)
	if err != nil {
		return nil, err
	}
	if err := applyAuthoritativeProjection(&state, metadata); err != nil {
		_ = journal.Close()
		return nil, err
	}
	thread := &Thread{ID: id, Dir: dir, journal: journal, state: state, store: s}
	thread.refreshPublicLocked()
	if _, err := s.loadOrRebuildIndexLocked(); err != nil {
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

func (s *Store) updateProjectionLocked() error {
	revision := uint64(0)
	if index, err := readIndexFile(s.IndexPath()); err == nil && index.Version == IndexVersion {
		revision = index.Revision
	}
	projections, err := s.scanProjectionFilesLocked()
	if err != nil {
		return err
	}
	_, err = s.writeProjectionIndexLocked(projections, revision)
	return err
}

func (s *Store) loadOrRebuildIndexLocked() (Index, error) {
	projections, err := s.scanProjectionFilesLocked()
	if err != nil {
		return Index{}, err
	}
	index, err := readIndexFile(s.IndexPath())
	if err == nil {
		if index.Version == IndexVersion && validIndexLifecycle(index) && indexMatchesProjections(index, projections) {
			return index, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return Index{}, err
		}
	}
	revision := uint64(0)
	if err == nil && index.Version == IndexVersion {
		revision = index.Revision
	}
	return s.writeProjectionIndexLocked(projections, revision)
}

func readIndexFile(path string) (Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Index{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var index Index
	if err := decoder.Decode(&index); err != nil {
		return Index{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Index{}, err
	}
	return index, nil
}

func indexMatchesProjections(index Index, projections []Projection) bool {
	if len(index.Threads) != len(projections) {
		return false
	}
	want := make([]IndexEntry, len(projections))
	for i := range projections {
		want[i] = indexEntryFromProjection(projections[i])
	}
	sortIndexEntries(want)
	return reflect.DeepEqual(index.Threads, want)
}

func validIndexLifecycle(index Index) bool {
	if index.Revision == 0 || index.UpdatedAt.IsZero() {
		return false
	}
	for _, entry := range index.Threads {
		if !ValidID(entry.ThreadID) || entry.Alias == "" || entry.ThreadRevision == 0 || entry.CurrentGenerationID == "" {
			return false
		}
		switch entry.RetentionState {
		case RetentionActive:
			if entry.ArchivedAt != nil || (entry.ExecutionState != ExecutionIdle && entry.ExecutionState != ExecutionWorking && entry.ExecutionState != ExecutionFailed) {
				return false
			}
		case RetentionArchived:
			if entry.ArchivedAt == nil || entry.ExecutionState != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (s *Store) rebuildIndexLocked() (Index, error) {
	projections, err := s.scanProjectionFilesLocked()
	if err != nil {
		return Index{}, err
	}
	return s.writeProjectionIndexLocked(projections, 0)
}

func (s *Store) scanProjectionFilesLocked() ([]Projection, error) {
	var projections []Projection
	seen := map[string]struct{}{}
	for _, root := range []string{s.ThreadsDir(), s.ArchiveDir()} {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !ValidID(entry.Name()) {
				continue
			}
			if _, exists := seen[entry.Name()]; exists {
				return nil, fmt.Errorf("%w: duplicate Thread directory %s", ErrInvalidMetadata, entry.Name())
			}
			projection, err := readProjectionFile(filepath.Join(root, entry.Name()), entry.Name())
			if err != nil {
				return nil, err
			}
			archivedNamespace := root == s.ArchiveDir()
			if (projection.RetentionState == RetentionArchived) != archivedNamespace {
				return nil, fmt.Errorf("%w for %s: retention state does not match directory namespace", ErrInvalidMetadata, entry.Name())
			}
			seen[entry.Name()] = struct{}{}
			projections = append(projections, projection)
		}
	}
	if err := validateProjectionSet(projections); err != nil {
		return nil, err
	}
	return projections, nil
}

func validateProjectionSet(projections []Projection) error {
	ids := make(map[string]string, len(projections))
	parents := make(map[string]string, len(projections))
	for _, projection := range projections {
		ids[strings.ToLower(projection.ThreadID)] = projection.ThreadID
		parents[projection.ThreadID] = projection.ParentThreadID
	}
	_, hasMain := ids[MainID]

	for index, projection := range projections {
		for _, candidate := range projections {
			if strings.EqualFold(projection.Alias, candidate.ThreadID) {
				return fmt.Errorf("%w: alias %q conflicts with Thread ID %s", ErrInvalidMetadata, projection.Alias, candidate.ThreadID)
			}
		}
		for previous := 0; previous < index; previous++ {
			if strings.EqualFold(projection.Alias, projections[previous].Alias) {
				return fmt.Errorf("%w: alias %q is shared by Threads %s and %s", ErrInvalidMetadata, projection.Alias, projections[previous].ThreadID, projection.ThreadID)
			}
		}
		if projection.ThreadID != MainID && hasMain {
			if _, exists := ids[strings.ToLower(projection.ParentThreadID)]; !exists {
				return fmt.Errorf("%w: Thread %s has missing parent %s", ErrInvalidMetadata, projection.ThreadID, projection.ParentThreadID)
			}
		}
	}

	for id := range parents {
		seen := map[string]struct{}{}
		for current := id; current != MainID; {
			if _, exists := seen[current]; exists {
				return fmt.Errorf("%w: parent cycle includes Thread %s", ErrInvalidMetadata, current)
			}
			seen[current] = struct{}{}
			parent, exists := parents[current]
			if !exists {
				break
			}
			current = parent
		}
	}
	return nil
}

func (s *Store) writeProjectionIndexLocked(projections []Projection, revision uint64) (Index, error) {
	index := Index{Version: IndexVersion, Revision: revision, UpdatedAt: NewTimestamp(s.now())}
	for _, projection := range projections {
		index.Threads = append(index.Threads, indexEntryFromProjection(projection))
	}
	if err := s.writeIndexLocked(&index); err != nil {
		return Index{}, err
	}
	return index, nil
}

func (s *Store) writeIndexLocked(index *Index) error {
	index.Version = IndexVersion
	index.Revision++
	index.UpdatedAt = NewTimestamp(s.now())
	sortIndexEntries(index.Threads)
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if s.writeIndex != nil {
		return s.writeIndex(s.IndexPath(), data)
	}
	return homestore.WriteFileAtomic(s.IndexPath(), data, 0o600, 0o755)
}

func sortIndexEntries(entries []IndexEntry) {
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if (left.ThreadID == MainID) != (right.ThreadID == MainID) {
			return left.ThreadID == MainID
		}
		if (left.RetentionState == RetentionActive) != (right.RetentionState == RetentionActive) {
			return left.RetentionState == RetentionActive
		}
		if !left.LastActivityAt.Equal(right.LastActivityAt.Time) {
			return left.LastActivityAt.After(right.LastActivityAt.Time)
		}
		return left.ThreadID < right.ThreadID
	})
}

func indexEntryFromProjection(projection Projection) IndexEntry {
	entry := IndexEntry{
		ThreadID:            projection.ThreadID,
		Alias:               projection.Alias,
		ParentThreadID:      projection.ParentThreadID,
		ArchivedAt:          projection.ArchivedAt,
		CreatedAt:           projection.CreatedAt,
		LastActivityAt:      projection.LastActivityAt,
		RetentionState:      projection.RetentionState,
		ExecutionState:      projection.ExecutionState,
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
