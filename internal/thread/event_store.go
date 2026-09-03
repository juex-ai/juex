package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/jsonl"
)

const generationsDirectory = "generations"

// EventStore is the sole production owner of a Thread's Generation journals.
// Thread facts and sequences are validated here, while jsonl.File owns raw
// append durability, repair, and bounded file reads.
type EventStore struct {
	threadDir string
	threadID  string
	currentID string
	current   *jsonl.File
	size      int64
	nextSeq   uint64
	now       func() time.Time
}

type stagedGeneration struct {
	id     string
	path   string
	file   *jsonl.File
	commit scannedCommit
}

// GenerationJournal identifies one registered chronological file.
type GenerationJournal struct {
	GenerationID string `json:"generation_id"`
	Path         string `json:"path"`
}

func createEventStore(threadDir, threadID, alias, parentThreadID string, now func() time.Time) (*EventStore, scannedCommit, error) {
	if !ValidID(threadID) {
		return nil, scannedCommit{}, fmt.Errorf("%w: %q", ErrInvalidID, threadID)
	}
	if now == nil {
		now = time.Now
	}
	store := &EventStore{
		threadDir: threadDir,
		threadID:  threadID,
		currentID: InitialGeneration,
		nextSeq:   1,
		now:       now,
	}
	path := store.generationPath(InitialGeneration)
	if err := ensureGenerationTargetAbsent(path); err != nil {
		return nil, scannedCommit{}, err
	}
	file, err := jsonl.Open(path)
	if err != nil {
		return nil, scannedCommit{}, err
	}
	store.current = file
	commit, err := store.prepareCommit([]Fact{{
		Type: FactThreadCreated, ThreadID: threadID, Alias: alias,
		ParentThreadID: parentThreadID, GenerationID: InitialGeneration,
	}})
	if err != nil {
		return nil, scannedCommit{}, errors.Join(err, cleanupGenerationFile(path, file))
	}
	scanned, err := store.appendCurrent(commit)
	if err != nil {
		return nil, scannedCommit{}, errors.Join(err, cleanupGenerationFile(path, file))
	}
	return store, scanned, nil
}

func openEventStore(threadDir, threadID string, metadata Projection, now func() time.Time) (*EventStore, ReplayState, bool, error) {
	if now == nil {
		now = time.Now
	}
	if err := recoverGenerationLayout(threadDir, metadata.Generations); err != nil {
		return nil, ReplayState{}, false, err
	}
	currentID := metadata.CurrentGeneration.ID
	file, commits, err := openGenerationFile(threadDir, threadID, metadata.CurrentGeneration)
	if err != nil {
		return nil, ReplayState{}, false, err
	}
	published, tail, err := splitCommitsAtCursor(threadID, commits, metadata.EventCursor)
	if err != nil {
		_ = file.Close()
		return nil, ReplayState{}, false, err
	}
	state, err := replayCurrentGeneration(threadID, metadata, published)
	if err != nil {
		_ = file.Close()
		return nil, ReplayState{}, false, err
	}
	last := commits[len(commits)-1]
	store := &EventStore{
		threadDir: threadDir,
		threadID:  threadID,
		currentID: currentID,
		current:   file,
		size:      file.Size(),
		nextSeq:   last.Seq + 1,
		now:       now,
	}
	recovered, err := store.recoverUsageAggregate(&state, metadata.EventCursor)
	if err != nil {
		_ = file.Close()
		return nil, ReplayState{}, false, err
	}
	if len(tail) == 1 {
		if err := applyCommit(threadID, &state, tail[0]); err != nil {
			_ = file.Close()
			return nil, ReplayState{}, false, fmt.Errorf("%w in %s at sequence %d: %v", ErrCorruptJournal, tail[0].GenerationID, tail[0].Seq, err)
		}
		recovered = true
	}
	if recovered {
		at := NewTimestamp(now())
		if at.Before(state.Projection.UpdatedAt.Time) {
			at = state.Projection.UpdatedAt
		}
		advanceProjectionRevision(&state.Projection, at)
	}
	if err := validateProjectionMetadata(state.Projection, threadID); err != nil {
		_ = file.Close()
		return nil, ReplayState{}, false, err
	}
	return store, state, recovered, nil
}

func splitCommitsAtCursor(threadID string, commits []scannedCommit, cursor EventCursor) ([]scannedCommit, []scannedCommit, error) {
	for index, commit := range commits {
		if commit.GenerationID != cursor.GenerationID || commit.Seq != cursor.Seq || commit.EndOffset != cursor.Offset {
			continue
		}
		tail := commits[index+1:]
		if len(tail) > 1 {
			return nil, nil, fmt.Errorf("%w for %s: current Generation has more than one unpublished commit", ErrInvalidMetadata, threadID)
		}
		if len(tail) == 1 && !usageOnlyCommit(tail[0]) {
			return nil, nil, fmt.Errorf("%w for %s: current Generation has a non-Usage unpublished commit", ErrInvalidMetadata, threadID)
		}
		return commits[:index+1], tail, nil
	}
	return nil, nil, fmt.Errorf("%w for %s: EventStore cursor is not a complete current-Generation record", ErrInvalidMetadata, threadID)
}

func usageOnlyCommit(commit scannedCommit) bool {
	if len(commit.Facts) == 0 {
		return false
	}
	for _, fact := range commit.Facts {
		if fact.Type != FactUsageRecorded {
			return false
		}
	}
	return true
}

func (s *EventStore) recoverUsageAggregate(state *ReplayState, through EventCursor) (bool, error) {
	if state == nil {
		return false, fmt.Errorf("%w: nil replay state", ErrInvalidTransition)
	}
	after := state.Projection.UsageAggregatedThrough
	if after == through {
		return false, nil
	}
	err := s.visitCommitsAfterCursor(state.Projection.Generations, after, through, func(commit scannedCommit) error {
		for _, fact := range commit.Facts {
			if fact.Type == FactUsageRecorded {
				state.Projection.TokenUsage.Add(fact.ModelRef, *fact.Usage)
			}
		}
		state.Projection.UsageAggregatedThrough = EventCursor{
			GenerationID: commit.GenerationID,
			Seq:          commit.Seq,
			Offset:       commit.EndOffset,
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if state.Projection.UsageAggregatedThrough != through {
		return false, fmt.Errorf("%w for %s: Usage aggregation scan did not reach EventStore cursor", ErrInvalidMetadata, s.threadID)
	}
	return true, nil
}

func (s *EventStore) visitCommitsAfterCursor(
	generations []GenerationProjection,
	after EventCursor,
	through EventCursor,
	visit func(scannedCommit) error,
) error {
	startIndex, endIndex := -1, -1
	for index, generation := range generations {
		if generation.ID == after.GenerationID {
			startIndex = index
		}
		if generation.ID == through.GenerationID {
			endIndex = index
		}
	}
	if startIndex < 0 || endIndex < startIndex {
		return fmt.Errorf("%w for %s: invalid Usage cursor Generation range", ErrInvalidMetadata, s.threadID)
	}
	selected := generations[startIndex : endIndex+1]
	captured, err := captureGenerationHandles(s.threadDir, selected, through.GenerationID, through.Offset)
	if err != nil {
		return err
	}
	defer func() { _ = closeCapturedGenerations(captured) }()
	wantSeq := after.Seq + 1
	for index := range captured {
		generation := captured[index]
		start := int64(0)
		if index == 0 {
			start = after.Offset
		}
		end := generation.End
		if index == len(captured)-1 {
			end = through.Offset
		}
		committed, err := generation.file.ReadForwardTo(start, end, func(record jsonl.Record) error {
			commit, err := decodeGenerationCommit(generation.ID, record)
			if err != nil {
				return err
			}
			if commit.Seq != wantSeq {
				return fmt.Errorf("%w in %s: commit sequence %d, want %d", ErrCorruptJournal, generation.ID, commit.Seq, wantSeq)
			}
			if err := validateCommit(s.threadID, commit.Commit); err != nil {
				return fmt.Errorf("%w in %s at sequence %d: %v", ErrCorruptJournal, generation.ID, commit.Seq, err)
			}
			if err := visit(commit); err != nil {
				return err
			}
			wantSeq++
			return nil
		})
		if err != nil {
			return wrapGenerationError(generation.ID, err)
		}
		if committed != end {
			return fmt.Errorf("%w in %s: Usage scan stopped at offset %d, want %d", ErrCorruptJournal, generation.ID, committed, end)
		}
	}
	if wantSeq != through.Seq+1 {
		return fmt.Errorf("%w for %s: Usage scan ended before sequence %d", ErrInvalidMetadata, s.threadID, through.Seq)
	}
	return nil
}

func (s *EventStore) prepareCommit(facts []Fact) (Commit, error) {
	if s == nil || s.current == nil {
		return Commit{}, fmt.Errorf("thread: EventStore closed")
	}
	commit := Commit{
		Version: JournalVersion,
		Seq:     s.nextSeq,
		At:      NewTimestamp(s.now()),
		Facts:   append([]Fact(nil), facts...),
	}
	if err := validateCommit(s.threadID, commit); err != nil {
		return Commit{}, err
	}
	data, err := json.Marshal(commit)
	if err != nil {
		return Commit{}, err
	}
	if len(data) > maxCommitBytes {
		return Commit{}, fmt.Errorf("thread: commit exceeds %d bytes", maxCommitBytes)
	}
	return commit, nil
}

func (s *EventStore) appendCurrent(commit Commit) (scannedCommit, error) {
	if s == nil || s.current == nil {
		return scannedCommit{}, fmt.Errorf("thread: EventStore closed")
	}
	if commit.Seq != s.nextSeq {
		return scannedCommit{}, fmt.Errorf("%w: commit sequence %d, want %d", ErrCorruptJournal, commit.Seq, s.nextSeq)
	}
	data, err := json.Marshal(commit)
	if err != nil {
		return scannedCommit{}, err
	}
	batch, err := s.current.Append(data)
	if err != nil {
		return scannedCommit{}, wrapGenerationError(s.currentID, err)
	}
	s.size = batch.End
	s.nextSeq++
	return scannedCommit{
		Commit: commit, GenerationID: s.currentID,
		StartOffset: batch.Start, EndOffset: batch.End,
	}, nil
}

func (s *EventStore) stageNextGeneration(commit Commit, generationID string) (*stagedGeneration, error) {
	if s == nil || s.current == nil {
		return nil, fmt.Errorf("thread: EventStore closed")
	}
	if commit.Seq != s.nextSeq {
		return nil, fmt.Errorf("%w: commit sequence %d, want %d", ErrCorruptJournal, commit.Seq, s.nextSeq)
	}
	wantOrdinal, err := parseGenerationID(generationID)
	if err != nil {
		return nil, err
	}
	currentOrdinal, err := parseGenerationID(s.currentID)
	if err != nil || wantOrdinal != currentOrdinal+1 {
		return nil, fmt.Errorf("%w: Generation %q does not follow %q", ErrInvalidTransition, generationID, s.currentID)
	}
	path := s.generationPath(generationID)
	if err := ensureGenerationTargetAbsent(path); err != nil {
		return nil, err
	}
	file, err := jsonl.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(commit)
	if err != nil {
		return nil, errors.Join(err, cleanupGenerationFile(path, file))
	}
	batch, err := file.Append(data)
	if err != nil {
		return nil, errors.Join(wrapGenerationError(generationID, err), cleanupGenerationFile(path, file))
	}
	return &stagedGeneration{
		id: generationID, path: path, file: file,
		commit: scannedCommit{
			Commit: commit, GenerationID: generationID,
			StartOffset: batch.Start, EndOffset: batch.End,
		},
	}, nil
}

func (s *EventStore) activate(staged *stagedGeneration) error {
	if s == nil || staged == nil || staged.file == nil {
		return fmt.Errorf("thread: invalid staged Generation")
	}
	previous := s.current
	s.current = staged.file
	s.currentID = staged.id
	s.size = staged.commit.EndOffset
	s.nextSeq = staged.commit.Seq + 1
	staged.file = nil
	if previous == nil {
		return nil
	}
	return previous.Close()
}

func (s *EventStore) discard(staged *stagedGeneration) error {
	if staged == nil {
		return nil
	}
	var closeErr error
	if staged.file != nil {
		closeErr = staged.file.Close()
	}
	staged.file = nil
	removeErr := os.Remove(staged.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	var syncErr error
	if removeErr == nil {
		syncErr = homestore.SyncDir(filepath.Dir(staged.path))
	}
	return errors.Join(closeErr, removeErr, syncErr)
}

func (s *EventStore) CurrentGenerationJournalPath() string {
	if s == nil || s.currentID == "" {
		return ""
	}
	return s.generationPath(s.currentID)
}

func (s *EventStore) GenerationJournalPaths(generations []GenerationProjection) []string {
	if s == nil {
		return nil
	}
	paths := make([]string, len(generations))
	for index := range generations {
		paths[index] = s.generationPath(generations[index].ID)
	}
	return paths
}

func (s *EventStore) captureGenerations(generations []GenerationProjection) ([]capturedGeneration, error) {
	return captureGenerationHandles(s.threadDir, generations, s.currentID, s.size)
}

func captureGenerationHandles(
	threadDir string,
	generations []GenerationProjection,
	currentID string,
	currentEnd int64,
) ([]capturedGeneration, error) {
	paths := generationJournalPaths(threadDir, generations)
	captured := make([]capturedGeneration, len(paths))
	for index, path := range paths {
		end := currentEnd
		if generations[index].ID != currentID {
			info, err := os.Stat(path)
			if err != nil {
				_ = closeCapturedGenerations(captured[:index])
				return nil, wrapGenerationError(generations[index].ID, err)
			}
			end = info.Size()
		}
		file, err := jsonl.OpenSnapshot(path, end)
		if err != nil {
			_ = closeCapturedGenerations(captured[:index])
			return nil, wrapGenerationError(generations[index].ID, err)
		}
		captured[index] = capturedGeneration{
			GenerationProjection: generations[index],
			Path:                 path,
			End:                  end,
			file:                 file,
		}
	}
	return captured, nil
}

func closeCapturedGenerations(generations []capturedGeneration) error {
	var result error
	for index := range generations {
		if generations[index].file != nil {
			result = errors.Join(result, generations[index].file.Close())
		}
	}
	return result
}

func (s *EventStore) generationPath(generationID string) string {
	return filepath.Join(s.threadDir, generationsDirectory, generationID+".jsonl")
}

func (s *EventStore) relocate(threadDir string) {
	if s != nil {
		s.threadDir = filepath.Clean(threadDir)
	}
}

func (s *EventStore) Close() error {
	if s == nil || s.current == nil {
		return nil
	}
	err := s.current.Close()
	s.current = nil
	return err
}

// InspectGenerationJournals returns metadata-registered Generation paths
// without opening their JSONL contents. Diagnostic adapters use this instead
// of constructing storage paths themselves.
func InspectGenerationJournals(threadDir string) ([]GenerationJournal, error) {
	threadDir = filepath.Clean(threadDir)
	threadID := filepath.Base(threadDir)
	metadata, err := readProjectionFile(threadDir, threadID)
	if err != nil {
		return nil, err
	}
	paths := generationJournalPaths(threadDir, metadata.Generations)
	result := make([]GenerationJournal, len(paths))
	for index := range paths {
		result[index] = GenerationJournal{GenerationID: metadata.Generations[index].ID, Path: paths[index]}
	}
	return result, nil
}

func generationJournalPaths(threadDir string, generations []GenerationProjection) []string {
	paths := make([]string, len(generations))
	for index := range generations {
		paths[index] = filepath.Join(threadDir, generationsDirectory, generations[index].ID+".jsonl")
	}
	return paths
}

func readGenerationPrefix(generation capturedGeneration) ([]byte, error) {
	if generation.End < 0 {
		return nil, fmt.Errorf("%w in %s: invalid captured offset %d", ErrCorruptJournal, generation.ID, generation.End)
	}
	if generation.file == nil {
		return nil, fmt.Errorf("thread: Generation snapshot %s is closed", generation.ID)
	}
	data, err := generation.file.ReadBytesTo(generation.End)
	if err != nil {
		return nil, wrapGenerationError(generation.ID, err)
	}
	return data, nil
}

func visitCapturedGeneration(
	threadID string,
	generation capturedGeneration,
	wantSeq *uint64,
	visit func(Commit),
) error {
	if generation.file == nil {
		return fmt.Errorf("thread: Generation snapshot %s is closed", generation.ID)
	}
	_, err := generation.file.ReadForwardTo(0, generation.End, func(record jsonl.Record) error {
		commit, err := decodeGenerationCommit(generation.ID, record)
		if err != nil {
			return err
		}
		if commit.Seq != *wantSeq {
			return fmt.Errorf("%w in %s: commit sequence %d, want %d", ErrCorruptJournal, generation.ID, commit.Seq, *wantSeq)
		}
		if err := validateCommit(threadID, commit.Commit); err != nil {
			return fmt.Errorf("%w in %s at sequence %d: %v", ErrCorruptJournal, generation.ID, commit.Seq, err)
		}
		visit(commit.Commit)
		*wantSeq = *wantSeq + 1
		return nil
	})
	return wrapGenerationError(generation.ID, err)
}

func readGenerationReverse(
	threadID string,
	generation capturedGeneration,
	position int64,
	limit int,
	expectedBefore uint64,
) ([]scannedCommit, int64, uint64, error) {
	if generation.file == nil {
		return nil, position, expectedBefore, fmt.Errorf("thread: Generation snapshot %s is closed", generation.ID)
	}
	if position < 0 {
		position = generation.file.Size()
	}
	if position == 0 {
		return nil, 0, expectedBefore, nil
	}
	batch, err := generation.file.ReadReverse(position, limit)
	if err != nil {
		return nil, position, expectedBefore, wrapGenerationError(generation.ID, err)
	}
	if len(batch.Records) == 0 {
		return nil, position, expectedBefore, fmt.Errorf("%w in %s: empty reverse page before offset %d", ErrCorruptJournal, generation.ID, position)
	}
	commits := make([]scannedCommit, 0, len(batch.Records))
	for index := len(batch.Records) - 1; index >= 0; index-- {
		record := batch.Records[index]
		commit, err := decodeGenerationCommit(generation.ID, record)
		if err != nil {
			return nil, position, expectedBefore, err
		}
		if commit.Seq+1 != expectedBefore {
			return nil, position, expectedBefore, fmt.Errorf(
				"%w in %s: commit sequence %d is not before %d",
				ErrCorruptJournal, generation.ID, commit.Seq, expectedBefore,
			)
		}
		if err := validateCommit(threadID, commit.Commit); err != nil {
			return nil, position, expectedBefore, fmt.Errorf("%w in %s at sequence %d: %v", ErrCorruptJournal, generation.ID, commit.Seq, err)
		}
		commits = append(commits, commit)
		expectedBefore = commit.Seq
		position = record.Start
	}
	return commits, position, expectedBefore, nil
}

func ensureGenerationTargetAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("thread: Generation Journal already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func cleanupGenerationFile(path string, file *jsonl.File) error {
	var closeErr error
	if file != nil {
		closeErr = file.Close()
	}
	removeErr := os.Remove(path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	var syncErr error
	if removeErr == nil {
		syncErr = homestore.SyncDir(filepath.Dir(path))
	}
	return errors.Join(closeErr, removeErr, syncErr)
}

func recoverGenerationLayout(threadDir string, generations []GenerationProjection) error {
	dir := filepath.Join(threadDir, generationsDirectory)
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: generations path is not a directory", ErrCorruptJournal)
	}
	wanted := make(map[string]struct{}, len(generations))
	lastOrdinal := 0
	for _, generation := range generations {
		wanted[generation.ID+".jsonl"] = struct{}{}
		lastOrdinal = generation.Ordinal
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	removed := false
	seen := make(map[string]struct{}, len(generations))
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		generationID := strings.TrimSuffix(name, ".jsonl")
		ordinal, validIDErr := parseGenerationID(generationID)
		_, registered := wanted[name]
		if !registered {
			if validIDErr != nil || ordinal <= lastOrdinal || name != generationID+".jsonl" || !fileInfo.Mode().IsRegular() {
				return fmt.Errorf("%w: unexpected Generation entry %q", ErrCorruptJournal, name)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			removed = true
			continue
		}
		if !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: Generation Journal %q is not a regular file", ErrCorruptJournal, name)
		}
		seen[name] = struct{}{}
	}
	for name := range wanted {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("%w: registered Generation Journal %q is missing", ErrCorruptJournal, name)
		}
	}
	if removed {
		return homestore.SyncDir(dir)
	}
	return nil
}

func openGenerationFile(threadDir, threadID string, generation GenerationProjection) (*jsonl.File, []scannedCommit, error) {
	path := filepath.Join(threadDir, generationsDirectory, generation.ID+".jsonl")
	file, err := jsonl.Open(path)
	if err != nil {
		return nil, nil, wrapGenerationError(generation.ID, err)
	}
	commits, err := readGenerationCommits(file, threadID, generation)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, commits, nil
}

func readGenerationCommits(file *jsonl.File, threadID string, generation GenerationProjection) ([]scannedCommit, error) {
	var commits []scannedCommit
	wantSeq := generation.BoundarySeq
	_, err := file.ReadForward(0, func(record jsonl.Record) error {
		commit, err := decodeGenerationCommit(generation.ID, record)
		if err != nil {
			return err
		}
		if commit.Seq != wantSeq {
			return fmt.Errorf("%w in %s: commit sequence %d, want %d", ErrCorruptJournal, generation.ID, commit.Seq, wantSeq)
		}
		if err := validateCommit(threadID, commit.Commit); err != nil {
			return fmt.Errorf("%w in %s at sequence %d: %v", ErrCorruptJournal, generation.ID, commit.Seq, err)
		}
		commits = append(commits, commit)
		wantSeq++
		return nil
	})
	if err != nil {
		return nil, wrapGenerationError(generation.ID, err)
	}
	if len(commits) == 0 {
		return nil, fmt.Errorf("%w: Generation %s is empty", ErrCorruptJournal, generation.ID)
	}
	return commits, nil
}

func decodeGenerationCommit(generationID string, record jsonl.Record) (scannedCommit, error) {
	var commit Commit
	if err := decodeCommit(record.Data, &commit); err != nil {
		return scannedCommit{}, fmt.Errorf("%w in %s at offset %d: %v", ErrCorruptJournal, generationID, record.Start, err)
	}
	return scannedCommit{
		Commit: commit, GenerationID: generationID,
		StartOffset: record.Start, EndOffset: record.End,
	}, nil
}

func replayCurrentGeneration(threadID string, metadata Projection, commits []scannedCommit) (ReplayState, error) {
	if len(commits) == 0 {
		return ReplayState{}, fmt.Errorf("%w: current Generation is empty", ErrCorruptJournal)
	}
	first := commits[0]
	if metadata.CurrentGeneration.Ordinal == 1 {
		if len(first.Facts) != 1 || first.Facts[0].Type != FactThreadCreated || first.Seq != 1 {
			return ReplayState{}, fmt.Errorf("%w: initial Generation does not begin with Thread creation", ErrCorruptJournal)
		}
	} else {
		if len(first.Facts) != 1 || (first.Facts[0].Type != FactContextRenewed && first.Facts[0].Type != FactContextCompacted) {
			return ReplayState{}, fmt.Errorf("%w: Generation %s does not begin with a context boundary", ErrCorruptJournal, first.GenerationID)
		}
		fact := first.Facts[0]
		previous := metadata.Generations[metadata.CurrentGeneration.Ordinal-2]
		if fact.Seed == nil || fact.Seed.Version != ProjectionVersion ||
			fact.FromGenerationID != previous.ID || fact.ToGenerationID != metadata.CurrentGeneration.ID ||
			first.Seq != metadata.CurrentGeneration.BoundarySeq {
			return ReplayState{}, fmt.Errorf("%w: Generation %s boundary does not match metadata", ErrCorruptJournal, first.GenerationID)
		}
	}
	last := commits[len(commits)-1]
	if last.Seq != metadata.EventCursor.Seq || last.EndOffset != metadata.EventCursor.Offset ||
		last.GenerationID != metadata.EventCursor.GenerationID {
		return ReplayState{}, fmt.Errorf("%w for %s: current Generation exceeds or trails metadata cursor", ErrInvalidMetadata, threadID)
	}
	state := ReplayState{Projection: cloneProjection(metadata)}
	for index, commit := range commits {
		if err := applyRuntimeCommit(threadID, metadata.CurrentGeneration.ID, &state, commit, index == 0); err != nil {
			return ReplayState{}, fmt.Errorf("%w in %s at sequence %d: %v", ErrCorruptJournal, commit.GenerationID, commit.Seq, err)
		}
	}
	return state, nil
}

func applyRuntimeCommit(threadID, generationID string, state *ReplayState, commit scannedCommit, first bool) error {
	for _, fact := range commit.Facts {
		switch fact.Type {
		case FactThreadCreated:
			if !first || generationID != InitialGeneration || fact.ThreadID != threadID {
				return fmt.Errorf("%w: misplaced Thread creation", ErrInvalidTransition)
			}
		case FactContextRenewed, FactContextCompacted:
			if !first || fact.Seed == nil {
				return fmt.Errorf("%w: misplaced Generation boundary", ErrInvalidTransition)
			}
			applyGenerationSeed(state, *fact.Seed)
			activity := Activity{
				Type: fact.Type, At: commit.At, FromGenerationID: fact.FromGenerationID,
				ToGenerationID: fact.ToGenerationID, Summary: fact.Summary, Automatic: fact.Automatic,
			}
			state.Activities = []Activity{activity}
			if fact.Type == FactContextCompacted {
				state.CompactionCount++
			}
		case FactMessageAppended:
			if fact.GenerationID != generationID {
				return fmt.Errorf("%w: message Generation %q is not %q", ErrInvalidTransition, fact.GenerationID, generationID)
			}
			message := *fact.Message
			state.Messages = append(state.Messages, message)
			state.ProviderMessages = append(state.ProviderMessages, message)
		case FactEventRecorded:
			state.Events = append(state.Events, *fact.Event)
		case FactUsageRecorded:
			if fact.ContextUsage != nil {
				state.ContextUsage = cloneContextUsage(fact.ContextUsage)
			}
		}
	}
	return nil
}

func wrapGenerationError(generationID string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, jsonl.ErrCorrupt) || errors.Is(err, jsonl.ErrInvalidOffset) {
		return fmt.Errorf("%w in %s: %v", ErrCorruptJournal, generationID, err)
	}
	return fmt.Errorf("thread: Generation %s: %w", generationID, err)
}
