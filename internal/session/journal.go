package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/llm"
)

const sessionJournalVersion = 1

func acquireEventJournalLock(dir string) (*lockGuard, error) {
	guardPath := sessionLockGuardPath(dir)
	if err := os.MkdirAll(filepath.Dir(guardPath), 0o755); err != nil {
		return nil, fmt.Errorf("session: prepare event journal lock: %w", err)
	}
	guard, err := acquireLockGuard(guardPath)
	if err != nil {
		return nil, fmt.Errorf("session: lock event journal: %w", err)
	}
	return guard, nil
}

type journalKind string

const (
	journalKindConversation journalKind = "conversation"
	journalKindEvents       journalKind = "events"
)

type journalRecordHeader struct {
	JournalVersion int         `json:"journal_version"`
	Journal        journalKind `json:"journal"`
	SessionID      string      `json:"session_id"`
	Sequence       uint64      `json:"sequence"`
}

type journalRecordExpectation struct {
	kind      journalKind
	sessionID string
	sequence  uint64
}

type transcriptJournalRecord struct {
	journalRecordHeader
	llm.Message
}

type eventJournalRecord struct {
	journalRecordHeader
	events.Event
}

func marshalTranscriptJournalLine(sessionID string, sequence uint64, message llm.Message) ([]byte, error) {
	return marshalJSONLine(transcriptJournalRecord{
		journalRecordHeader: newJournalRecordHeader(journalKindConversation, sessionID, sequence),
		Message:             message,
	})
}

func decodeTranscriptJournalLine(
	line []byte,
	expect journalRecordExpectation,
) (llm.Message, journalRecordHeader, error) {
	var record transcriptJournalRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return llm.Message{}, journalRecordHeader{}, err
	}
	if err := validateJournalRecordHeader(record.journalRecordHeader, expect); err != nil {
		return llm.Message{}, journalRecordHeader{}, err
	}
	return record.Message, record.journalRecordHeader, nil
}

func marshalEventJournalLine(sessionID string, sequence uint64, event events.Event) ([]byte, error) {
	return marshalJSONLine(eventJournalRecord{
		journalRecordHeader: newJournalRecordHeader(journalKindEvents, sessionID, sequence),
		Event:               event,
	})
}

func decodeEventJournalLine(
	line []byte,
	expect journalRecordExpectation,
) (events.Event, journalRecordHeader, error) {
	var record eventJournalRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return events.Event{}, journalRecordHeader{}, err
	}
	if err := validateJournalRecordHeader(record.journalRecordHeader, expect); err != nil {
		return events.Event{}, journalRecordHeader{}, err
	}
	return record.Event, record.journalRecordHeader, nil
}

func newJournalRecordHeader(kind journalKind, sessionID string, sequence uint64) journalRecordHeader {
	return journalRecordHeader{
		JournalVersion: sessionJournalVersion,
		Journal:        kind,
		SessionID:      sessionID,
		Sequence:       sequence,
	}
}

func validateJournalRecordHeader(header journalRecordHeader, expect journalRecordExpectation) error {
	if header.JournalVersion != sessionJournalVersion {
		return fmt.Errorf("session: unsupported journal version %d", header.JournalVersion)
	}
	if header.Journal != expect.kind {
		return fmt.Errorf("session: journal kind %q, want %q", header.Journal, expect.kind)
	}
	if header.SessionID != expect.sessionID {
		return fmt.Errorf("session: journal session identity %q, want %q", header.SessionID, expect.sessionID)
	}
	if expect.sequence != 0 && header.Sequence != expect.sequence {
		return fmt.Errorf("session: journal sequence %d, want %d", header.Sequence, expect.sequence)
	}
	if header.Sequence == 0 {
		return fmt.Errorf("session: journal sequence must be positive")
	}
	return nil
}

type journalFileOps struct {
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	truncate func(string, int64) error
}

type journalRollbackError struct {
	err error
}

func (e *journalRollbackError) Error() string {
	return "journal rollback failed: " + e.err.Error()
}

func (e *journalRollbackError) Unwrap() error {
	return e.err
}

func defaultJournalFileOps() journalFileOps {
	return journalFileOps{
		write:    func(file *os.File, data []byte) (int, error) { return file.Write(data) },
		sync:     func(file *os.File) error { return file.Sync() },
		truncate: os.Truncate,
	}
}

func appendJournalBytesDurably(
	file *os.File,
	path string,
	offset int64,
	data []byte,
	ops journalFileOps,
) error {
	if file == nil {
		return fmt.Errorf("session: journal file closed")
	}
	ops = completeJournalFileOps(ops)
	written, err := ops.write(file, data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = ops.sync(file)
	}
	if err == nil {
		return nil
	}
	rollbackErr := rollbackJournalBytesDurably(file, path, offset, ops)
	if rollbackErr != nil {
		return errors.Join(err, &journalRollbackError{err: rollbackErr})
	}
	return err
}

func rollbackJournalBytesDurably(file *os.File, path string, offset int64, ops journalFileOps) error {
	return truncateJournalTailDurably(file, path, offset, ops)
}

func truncateJournalTailDurably(file *os.File, path string, offset int64, ops journalFileOps) error {
	ops = completeJournalFileOps(ops)
	if err := ops.truncate(path, offset); err != nil {
		return fmt.Errorf("truncate to byte %d: %w", offset, err)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to byte %d: %w", offset, err)
	}
	if err := ops.sync(file); err != nil {
		return fmt.Errorf("sync truncate at byte %d: %w", offset, err)
	}
	return nil
}

func completeJournalFileOps(ops journalFileOps) journalFileOps {
	defaults := defaultJournalFileOps()
	if ops.write == nil {
		ops.write = defaults.write
	}
	if ops.sync == nil {
		ops.sync = defaults.sync
	}
	if ops.truncate == nil {
		ops.truncate = defaults.truncate
	}
	return ops
}

type tornJournalTailError struct {
	path   string
	offset int64
}

func (e *tornJournalTailError) Error() string {
	return fmt.Sprintf("session: torn journal tail in %s at byte %d", e.path, e.offset)
}

func repairTornJournalTail(path string, offset int64, ops journalFileOps) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("session: open torn journal %s for repair: %w", path, err)
	}
	defer file.Close()
	if err := truncateJournalTailDurably(file, path, offset, ops); err != nil {
		return fmt.Errorf("session: repair torn journal %s: %w", path, err)
	}
	return nil
}

func journalSessionID(path string) string {
	return filepath.Base(filepath.Dir(path))
}

func openJournalForAppend(path string, create bool) (*os.File, error) {
	created := false
	if create {
		_, err := os.Stat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			created = true
		case err != nil:
			return nil, err
		}
	}
	flags := os.O_APPEND | os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, err
	}
	if !created {
		return file, nil
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("session: sync new journal %s: %w", path, err)
	}
	if err := homestore.SyncDir(filepath.Dir(path)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("session: sync journal directory %s: %w", filepath.Dir(path), err)
	}
	return file, nil
}
