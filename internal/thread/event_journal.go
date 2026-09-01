package thread

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/events"
)

// EventJournalSnapshot is a stable, bounded view of the durable Thread
// journal. The underlying file is opened while the Thread is locked, and the
// captured size prevents later appends from entering the replay.
type EventJournalSnapshot struct {
	file     *os.File
	threadID string
	size     int64
	lastSeq  uint64
}

// CaptureEventJournal captures the committed journal boundary without
// decoding it. Callers can release their commit barrier before calling Events.
func (t *Thread) CaptureEventJournal() (*EventJournalSnapshot, error) {
	if t == nil {
		return nil, fmt.Errorf("thread: nil Thread")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.journal == nil {
		return nil, fmt.Errorf("thread: closed")
	}
	return t.journal.captureEventSnapshot()
}

func (j *Journal) captureEventSnapshot() (*EventJournalSnapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.file == nil {
		return nil, fmt.Errorf("thread: journal closed")
	}
	file, err := os.Open(j.path)
	if err != nil {
		return nil, err
	}
	return &EventJournalSnapshot{
		file:     file,
		threadID: j.threadID,
		size:     j.size,
		lastSeq:  j.nextSeq - 1,
	}, nil
}

// Events validates the captured journal prefix and returns every durable
// event.recorded fact in commit order, including events older than the latest
// projection checkpoint.
func (s *EventJournalSnapshot) Events() ([]events.Event, error) {
	if s == nil || s.file == nil {
		return nil, fmt.Errorf("thread: event journal snapshot closed")
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(s.file, 0, s.size), 64*1024)
	var (
		result  []events.Event
		offset  int64
		wantSeq uint64 = 1
	)
	for offset < s.size {
		line, err := reader.ReadBytes('\n')
		start := offset
		offset += int64(len(line))
		if err != nil {
			return nil, fmt.Errorf("%w: incomplete captured commit at offset %d: %v", ErrCorruptJournal, start, err)
		}
		if len(line) > maxCommitBytes {
			return nil, fmt.Errorf("%w: commit at offset %d exceeds %d bytes", ErrCorruptJournal, start, maxCommitBytes)
		}
		var commit Commit
		if err := decodeCommit(line, &commit); err != nil {
			return nil, fmt.Errorf("%w at offset %d: %v", ErrCorruptJournal, start, err)
		}
		if commit.Seq != wantSeq {
			return nil, fmt.Errorf("%w: commit sequence %d, want %d", ErrCorruptJournal, commit.Seq, wantSeq)
		}
		if err := validateCommit(s.threadID, commit); err != nil {
			return nil, fmt.Errorf("%w at sequence %d: %v", ErrCorruptJournal, commit.Seq, err)
		}
		for _, fact := range commit.Facts {
			if fact.Type == FactEventRecorded {
				result = append(result, *fact.Event)
			}
		}
		wantSeq++
	}
	if offset != s.size || wantSeq-1 != s.lastSeq {
		return nil, fmt.Errorf("%w: captured boundary ended at offset %d sequence %d, want offset %d sequence %d",
			ErrCorruptJournal, offset, wantSeq-1, s.size, s.lastSeq)
	}
	return result, nil
}

func (s *EventJournalSnapshot) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

var _ io.Closer = (*EventJournalSnapshot)(nil)
