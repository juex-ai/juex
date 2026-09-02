package thread

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	journalFile       = "journal.jsonl"
	projectionFile    = "thread.json"
	maxCommitBytes    = 16 << 20
	maxFactsPerCommit = 64
)

type journalOps struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	truncate func(*os.File, int64) error
}

func (ops journalOps) writeBytes(file *os.File, data []byte) (int, error) {
	if ops.write != nil {
		return ops.write(file, data)
	}
	return file.Write(data)
}

func (ops journalOps) syncFile(file *os.File) error {
	if ops.sync != nil {
		return ops.sync(file)
	}
	return file.Sync()
}

func (ops journalOps) truncateFile(file *os.File, size int64) error {
	if ops.truncate != nil {
		return ops.truncate(file, size)
	}
	return file.Truncate(size)
}

type Journal struct {
	mu       sync.Mutex
	path     string
	threadID string
	file     *os.File
	nextSeq  uint64
	size     int64
	now      func() time.Time
	ops      journalOps
	closed   bool
}

type scannedCommit struct {
	Commit
	StartOffset int64
	EndOffset   int64
}

func openJournal(path, threadID string, now func() time.Time) (*Journal, []scannedCommit, error) {
	if !ValidID(threadID) {
		return nil, nil, fmt.Errorf("%w: %q", ErrInvalidID, threadID)
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	commits, size, err := scanJournal(file, threadID, true)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	next := uint64(1)
	if len(commits) > 0 {
		next = commits[len(commits)-1].Seq + 1
	}
	return &Journal{
		path:     path,
		threadID: threadID,
		file:     file,
		nextSeq:  next,
		size:     size,
		now:      now,
	}, commits, nil
}

func openJournalForReplay(path, threadID string, now func() time.Time) (*Journal, ReplayState, error) {
	if !ValidID(threadID) {
		return nil, ReplayState{}, fmt.Errorf("%w: %q", ErrInvalidID, threadID)
	}
	if now == nil {
		now = time.Now
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, ReplayState{}, err
	}
	size, err := repairTornJournalTail(file)
	if err != nil {
		_ = file.Close()
		return nil, ReplayState{}, err
	}
	state, nextSeq, found, err := replayFromLatestCheckpoint(file, threadID, size)
	if err != nil {
		_ = file.Close()
		return nil, ReplayState{}, err
	}
	if !found {
		commits, scannedSize, scanErr := scanJournal(file, threadID, false)
		if scanErr != nil {
			_ = file.Close()
			return nil, ReplayState{}, scanErr
		}
		size = scannedSize
		state, err = replay(threadID, commits)
		if err != nil {
			_ = file.Close()
			return nil, ReplayState{}, err
		}
		nextSeq = state.Projection.Journal.ProjectedSeq + 1
	}
	return &Journal{
		path: path, threadID: threadID, file: file,
		nextSeq: nextSeq, size: size, now: now,
	}, state, nil
}

func repairTornJournalTail(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	last := []byte{0}
	if _, err := file.ReadAt(last, size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	position := size
	truncateAt := int64(0)
	for position > 0 {
		start := position - reverseReadBlock
		if start < 0 {
			start = 0
		}
		buffer := make([]byte, position-start)
		if _, err := file.ReadAt(buffer, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if index := bytes.LastIndexByte(buffer, '\n'); index >= 0 {
			truncateAt = start + int64(index) + 1
			break
		}
		position = start
	}
	if err := file.Truncate(truncateAt); err != nil {
		return 0, err
	}
	if err := file.Sync(); err != nil {
		return 0, err
	}
	return truncateAt, nil
}

func replayFromLatestCheckpoint(file *os.File, threadID string, size int64) (ReplayState, uint64, bool, error) {
	position := size
	newerSeq := uint64(0)
	var reverseSuffix []scannedCommit
	for position > 0 {
		end := position
		line, start, err := previousJournalLine(file, position)
		if err != nil {
			return ReplayState{}, 0, false, err
		}
		position = start
		var commit Commit
		if err := decodeCommit(line, &commit); err != nil {
			return ReplayState{}, 0, false, fmt.Errorf("%w at offset %d: %v", ErrCorruptJournal, start, err)
		}
		if err := validateCommit(threadID, commit); err != nil {
			return ReplayState{}, 0, false, fmt.Errorf("%w at sequence %d: %v", ErrCorruptJournal, commit.Seq, err)
		}
		if newerSeq != 0 && commit.Seq+1 != newerSeq {
			return ReplayState{}, 0, false, fmt.Errorf("%w: commit sequence %d is not before %d", ErrCorruptJournal, commit.Seq, newerSeq)
		}
		newerSeq = commit.Seq
		scanned := scannedCommit{Commit: commit, StartOffset: start, EndOffset: end}
		if len(commit.Facts) == 1 && commit.Facts[0].Type == FactProjectionCheck {
			checkpoint := commit.Facts[0].Checkpoint
			if checkpoint == nil {
				return ReplayState{}, 0, false, fmt.Errorf("%w: empty checkpoint at sequence %d", ErrCorruptJournal, commit.Seq)
			}
			state, err := replayStateFromCheckpoint(threadID, scanned, *checkpoint)
			if err != nil {
				return ReplayState{}, 0, false, err
			}
			for index := len(reverseSuffix) - 1; index >= 0; index-- {
				if err := applyCommit(threadID, &state, reverseSuffix[index]); err != nil {
					return ReplayState{}, 0, false, fmt.Errorf("%w at sequence %d: %v", ErrCorruptJournal, reverseSuffix[index].Seq, err)
				}
			}
			return state, state.Projection.Journal.ProjectedSeq + 1, true, nil
		}
		reverseSuffix = append(reverseSuffix, scanned)
	}
	return ReplayState{}, 0, false, nil
}

func scanJournal(file *os.File, threadID string, repairTail bool) ([]scannedCommit, int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	var (
		commits []scannedCommit
		offset  int64
		wantSeq uint64 = 1
	)
	for {
		line, err := reader.ReadBytes('\n')
		start := offset
		offset += int64(len(line))
		if errors.Is(err, io.EOF) && len(line) > 0 {
			if !repairTail {
				return nil, 0, fmt.Errorf("%w: torn final line at offset %d", ErrCorruptJournal, start)
			}
			if truncateErr := file.Truncate(start); truncateErr != nil {
				return nil, 0, errors.Join(
					fmt.Errorf("%w: torn final line at offset %d", ErrCorruptJournal, start),
					truncateErr,
				)
			}
			if syncErr := file.Sync(); syncErr != nil {
				return nil, 0, syncErr
			}
			offset = start
			break
		}
		if len(line) > maxCommitBytes {
			return nil, 0, fmt.Errorf("%w: commit at offset %d exceeds %d bytes", ErrCorruptJournal, start, maxCommitBytes)
		}
		if len(line) > 0 {
			var commit Commit
			if decodeErr := decodeCommit(line, &commit); decodeErr != nil {
				return nil, 0, fmt.Errorf("%w at offset %d: %v", ErrCorruptJournal, start, decodeErr)
			}
			if commit.Seq != wantSeq {
				return nil, 0, fmt.Errorf("%w: commit sequence %d, want %d", ErrCorruptJournal, commit.Seq, wantSeq)
			}
			if err := validateCommit(threadID, commit); err != nil {
				return nil, 0, fmt.Errorf("%w at sequence %d: %v", ErrCorruptJournal, commit.Seq, err)
			}
			commits = append(commits, scannedCommit{Commit: commit, StartOffset: start, EndOffset: offset})
			wantSeq++
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
	}
	return commits, offset, nil
}

func decodeCommit(line []byte, commit *Commit) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(commit); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateCommit(threadID string, commit Commit) error {
	if commit.Version != JournalVersion {
		return fmt.Errorf("unsupported version %d", commit.Version)
	}
	if commit.Seq == 0 {
		return fmt.Errorf("zero sequence")
	}
	if commit.At.IsZero() {
		return fmt.Errorf("zero timestamp")
	}
	if len(commit.Facts) == 0 || len(commit.Facts) > maxFactsPerCommit {
		return fmt.Errorf("fact count %d outside 1..%d", len(commit.Facts), maxFactsPerCommit)
	}
	for i, fact := range commit.Facts {
		if fact.Type == FactProjectionCheck && len(commit.Facts) != 1 {
			return fmt.Errorf("checkpoint must be the only fact in its commit")
		}
		if err := validateFactShape(threadID, fact); err != nil {
			return fmt.Errorf("fact %d: %w", i, err)
		}
	}
	return nil
}

func validateFactShape(threadID string, fact Fact) error {
	if fact.Type == "" {
		return fmt.Errorf("%w: empty type", ErrInvalidFact)
	}
	if fact.ThreadID != "" && fact.ThreadID != threadID {
		return fmt.Errorf("%w: fact thread %q does not match %q", ErrInvalidFact, fact.ThreadID, threadID)
	}
	switch fact.Type {
	case FactThreadCreated:
		if fact.ThreadID != threadID || fact.GenerationID != InitialGeneration || fact.Alias == "" {
			return fmt.Errorf("%w: invalid thread.created", ErrInvalidFact)
		}
		if threadID == MainID && (fact.Alias != MainAlias || fact.ParentThreadID != "") {
			return fmt.Errorf("%w: invalid Main identity", ErrInvalidFact)
		}
		if threadID != MainID && !ValidID(fact.ParentThreadID) {
			return fmt.Errorf("%w: Worker parent is invalid", ErrInvalidFact)
		}
	case FactMessageAppended:
		if fact.Message == nil || fact.Message.Role == "" || fact.GenerationID == "" {
			return fmt.Errorf("%w: invalid message.appended", ErrInvalidFact)
		}
	case FactEventRecorded:
		if fact.Event == nil || fact.Event.Type == "" {
			return fmt.Errorf("%w: invalid event.recorded", ErrInvalidFact)
		}
	case FactInputAccepted:
		if fact.InputID == "" {
			return fmt.Errorf("%w: input id required", ErrInvalidFact)
		}
	case FactInputRecorded:
		if fact.InputID == "" || len(fact.InputRecord) == 0 {
			return fmt.Errorf("%w: input record required", ErrInvalidFact)
		}
	case FactInputAttemptStart:
		if fact.InputID == "" || fact.AttemptID == "" || fact.GenerationID == "" || fact.TurnID == "" {
			return fmt.Errorf("%w: attempt identity required", ErrInvalidFact)
		}
	case FactInputAttemptDone, FactInputAttemptFailed, FactInputAttemptCancel, FactInputAttemptStop:
		if fact.InputID == "" || fact.AttemptID == "" {
			return fmt.Errorf("%w: attempt identity required", ErrInvalidFact)
		}
	case FactInputRequeued, FactInputCompleted, FactInputDeadLettered, FactInputCancelled, FactInputExpired:
		if fact.InputID == "" {
			return fmt.Errorf("%w: input id required", ErrInvalidFact)
		}
	case FactContextRenewed, FactContextCompacted:
		if fact.FromGenerationID == "" || fact.ToGenerationID == "" {
			return fmt.Errorf("%w: generation boundary required", ErrInvalidFact)
		}
		if fact.Type == FactContextCompacted && fact.Summary == nil {
			return fmt.Errorf("%w: compact summary required", ErrInvalidFact)
		}
	case FactNotesUpdated:
		if fact.Notes == nil {
			return fmt.Errorf("%w: notes required", ErrInvalidFact)
		}
	case FactUsageRecorded:
		if fact.Usage == nil && fact.ContextUsage == nil {
			return fmt.Errorf("%w: usage required", ErrInvalidFact)
		}
	case FactProjectionCheck:
		if fact.Checkpoint == nil || fact.Checkpoint.Version != ProjectionVersion {
			return fmt.Errorf("%w: checkpoint required", ErrInvalidFact)
		}
	case FactTurnStarted, FactTurnCompleted, FactTurnFailed, FactTurnCancelled, FactThreadSettled,
		FactGoalUpdated, FactGoalCleared, FactNotesCleared:
	default:
		return fmt.Errorf("%w: unknown fact type %q", ErrInvalidFact, fact.Type)
	}
	return nil
}

func (j *Journal) Append(facts ...Fact) (Commit, int64, error) {
	commit, start, _, err := j.appendValidated(facts, nil)
	return commit, start, err
}

func (j *Journal) appendValidated(facts []Fact, validate func(scannedCommit) error) (Commit, int64, int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.file == nil {
		return Commit{}, 0, 0, fmt.Errorf("thread: journal closed")
	}
	commit := Commit{
		Version: JournalVersion,
		Seq:     j.nextSeq,
		At:      NewTimestamp(j.now()),
		Facts:   append([]Fact(nil), facts...),
	}
	if err := validateCommit(j.threadID, commit); err != nil {
		return Commit{}, 0, 0, err
	}
	line, err := json.Marshal(commit)
	if err != nil {
		return Commit{}, 0, 0, err
	}
	line = append(line, '\n')
	if len(line) > maxCommitBytes {
		return Commit{}, 0, 0, fmt.Errorf("thread: commit exceeds %d bytes", maxCommitBytes)
	}
	info, err := j.file.Stat()
	if err != nil {
		return Commit{}, 0, 0, err
	}
	if info.Size() != j.size {
		return Commit{}, 0, 0, fmt.Errorf("%w: journal size changed from %d to %d", ErrCorruptJournal, j.size, info.Size())
	}
	start := j.size
	end := start + int64(len(line))
	if validate != nil {
		if err := validate(scannedCommit{Commit: commit, StartOffset: start, EndOffset: end}); err != nil {
			return Commit{}, 0, 0, err
		}
	}
	if _, err := j.file.Seek(start, io.SeekStart); err != nil {
		return Commit{}, 0, 0, err
	}
	written := 0
	for written < len(line) {
		n, writeErr := j.ops.writeBytes(j.file, line[written:])
		written += n
		if writeErr != nil || n == 0 {
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			return Commit{}, 0, 0, j.rollback(start, writeErr)
		}
	}
	if err := j.ops.syncFile(j.file); err != nil {
		return Commit{}, 0, 0, j.rollback(start, err)
	}
	j.size += int64(len(line))
	j.nextSeq++
	return commit, start, end, nil
}

func (j *Journal) rollback(offset int64, cause error) error {
	truncateErr := j.ops.truncateFile(j.file, offset)
	if _, seekErr := j.file.Seek(offset, io.SeekStart); seekErr != nil {
		truncateErr = errors.Join(truncateErr, seekErr)
	}
	if truncateErr == nil {
		truncateErr = j.ops.syncFile(j.file)
	}
	return errors.Join(cause, truncateErr)
}

func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}
