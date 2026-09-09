package thread

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/jsonl"
)

type capturedGeneration struct {
	GenerationProjection
	Path string
	End  int64
	file *jsonl.File
}

// EventStoreSnapshot is a stable all-Generation view. Historical files are
// immutable; End captures the only file that could still receive appends.
type EventStoreSnapshot struct {
	threadID    string
	generations []capturedGeneration
	lastSeq     uint64
	closed      bool
}

type GenerationJournalSnapshot struct {
	GenerationID string
	Path         string
	Data         []byte
}

func (t *Thread) CaptureEventStore() (*EventStoreSnapshot, error) {
	if t == nil {
		return nil, fmt.Errorf("thread: nil Thread")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.eventStore == nil {
		return nil, fmt.Errorf("thread: closed")
	}
	generations, err := t.eventStore.captureGenerations(t.state.Projection.Generations)
	if err != nil {
		return nil, err
	}
	return &EventStoreSnapshot{
		threadID: t.ID, generations: generations,
		lastSeq: t.state.Projection.EventCursor.Seq,
	}, nil
}

// CaptureEventStoreSnapshot captures the metadata-registered Generation
// journals without opening a mutable Thread or running lifecycle recovery.
// Read-only adapters such as debug bundles use it so observation cannot remove
// a writer's staged rollover file.
func CaptureEventStoreSnapshot(threadDir string) (*EventStoreSnapshot, error) {
	threadDir = filepath.Clean(threadDir)
	threadID := filepath.Base(threadDir)
	metadata, err := readProjectionFile(threadDir, threadID)
	if err != nil {
		return nil, err
	}
	generations, err := captureGenerationHandles(
		threadDir,
		metadata.Generations,
		metadata.CurrentGeneration.ID,
		metadata.EventCursor.Offset,
	)
	if err != nil {
		return nil, err
	}
	return &EventStoreSnapshot{
		threadID: threadID, generations: generations,
		lastSeq: metadata.EventCursor.Seq,
	}, nil
}

func (s *EventStoreSnapshot) GenerationJournals() ([]GenerationJournalSnapshot, error) {
	if s == nil || s.closed {
		return nil, fmt.Errorf("thread: EventStore snapshot closed")
	}
	result := make([]GenerationJournalSnapshot, len(s.generations))
	for index, generation := range s.generations {
		data, err := readGenerationPrefix(generation)
		if err != nil {
			return nil, err
		}
		result[index] = GenerationJournalSnapshot{
			GenerationID: generation.ID,
			Path:         generation.Path,
			Data:         data,
		}
	}
	return result, nil
}

func (s *EventStoreSnapshot) Events() ([]events.Event, error) {
	if s == nil || s.closed {
		return nil, fmt.Errorf("thread: EventStore snapshot closed")
	}
	var result []events.Event
	wantSeq := uint64(1)
	for _, generation := range s.generations {
		if err := visitCapturedGeneration(s.threadID, generation, &wantSeq, func(commit Commit) {
			for _, fact := range commit.Facts {
				if fact.Type == FactEventRecorded {
					result = append(result, *fact.Event)
				}
			}
		}); err != nil {
			return nil, err
		}
	}
	if wantSeq-1 != s.lastSeq {
		return nil, fmt.Errorf("%w: captured sequence ended at %d, want %d", ErrCorruptJournal, wantSeq-1, s.lastSeq)
	}
	return result, nil
}

// VisitAfter incrementally reads committed facts without opening a mutable
// Thread. The returned cursor belongs to this snapshot, including rollovers.
func (s *EventStoreSnapshot) VisitAfter(after EventCursor, visit func(Commit) error) (EventCursor, error) {
	if s == nil || s.closed {
		return EventCursor{}, fmt.Errorf("thread: EventStore snapshot closed")
	}
	startIndex := 0
	if after.GenerationID != "" {
		startIndex = -1
		for i, generation := range s.generations {
			if generation.ID == after.GenerationID {
				startIndex = i
				break
			}
		}
		if startIndex < 0 {
			return EventCursor{}, fmt.Errorf("%w: unknown cursor generation", ErrInvalidMetadata)
		}
	}
	wantSeq := after.Seq + 1
	for index := startIndex; index < len(s.generations); index++ {
		generation := s.generations[index]
		start := int64(0)
		if index == startIndex {
			start = after.Offset
		}
		end, err := generation.file.ReadForwardTo(start, generation.End, func(record jsonl.Record) error {
			commit, err := decodeGenerationCommit(generation.ID, record)
			if err != nil {
				return err
			}
			if commit.Seq != wantSeq {
				return fmt.Errorf("%w: sequence %d, want %d", ErrCorruptJournal, commit.Seq, wantSeq)
			}
			if err := validateCommit(s.threadID, commit.Commit); err != nil {
				return err
			}
			if err := visit(commit.Commit); err != nil {
				return err
			}
			wantSeq++
			return nil
		})
		if err != nil {
			return EventCursor{}, err
		}
		if end != generation.End {
			return EventCursor{}, fmt.Errorf("%w: incomplete snapshot", ErrCorruptJournal)
		}
	}
	if wantSeq-1 != s.lastSeq {
		return EventCursor{}, fmt.Errorf("%w: incomplete sequence", ErrCorruptJournal)
	}
	last := s.generations[len(s.generations)-1]
	return EventCursor{GenerationID: last.ID, Seq: s.lastSeq, Offset: last.End}, nil
}

func (s *EventStoreSnapshot) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	return closeCapturedGenerations(s.generations)
}

func (t *Thread) ReadEvents() ([]events.Event, error) {
	snapshot, err := t.CaptureEventStore()
	if err != nil {
		return nil, err
	}
	result, readErr := snapshot.Events()
	return result, errors.Join(readErr, snapshot.Close())
}

var _ io.Closer = (*EventStoreSnapshot)(nil)
