// Package session owns the in-memory conversation history for a single
// runtime session and persists every message + emitted event to jsonl files.
//
// File layout under <root>/<session_id>/:
//
//	conversation.jsonl   versioned, sequenced transcript records
//	events.jsonl         versioned, sequenced runtime event records
//
// The CLI and web server use Load to resume existing sessions.
package session

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	conversationFile     = "conversation.jsonl"
	transcriptAppendLock = "conversation.lock"
	eventsFile           = "events.jsonl"
)

type Session struct {
	ID           string
	Dir          string
	Alias        string
	Kind         string
	Active       bool
	History      []llm.Message
	TokenUsage   llm.Usage
	ContextUsage *llm.ContextUsage

	mu           sync.Mutex
	convFD       *os.File
	eventFD      *os.File
	transcript   transcriptIndex
	historyPath  string
	startedAtMS  int64
	lastActiveMS int64
	// Canonical transcript commits cannot be retried by their callers. Keep
	// failed derived writes resident until a later operation or Close repairs them.
	metadataDirty bool
	historyDirty  bool
	eventSequence uint64
	eventCatalog  events.SchemaCatalog
	journalOps    journalFileOps
	journalErr    error
	closed        bool
	closeOnce     sync.Once
	closeErr      error

	beforeTranscriptWrite        func()
	afterTranscriptPrewriteCheck func()
	afterTranscriptWrite         func()
	beforeRepairCheckpointSave   func()
}

type Options struct {
	Alias            string
	Kind             string
	Active           bool
	HistoryPath      string
	Lazy             bool
	RepairTranscript bool
	EventCatalog     events.SchemaCatalog
}

const scratchpadDirectory = "scratchpad"

// ScratchpadDir returns the session-local directory for long working material.
func ScratchpadDir(sessionDir string) string {
	return filepath.Join(sessionDir, scratchpadDirectory)
}

func (s *Session) ScratchpadDir() string {
	return ScratchpadDir(s.Dir)
}

func ensureScratchpadDir(sessionDir string) error {
	return os.MkdirAll(ScratchpadDir(sessionDir), 0o755)
}

// New creates a new session under rootDir. rootDir is created if missing.
func New(rootDir string) (*Session, error) {
	return NewWithOptions(rootDir, Options{})
}

func NewWithOptions(rootDir string, opts Options) (*Session, error) {
	id := newID()
	dir := filepath.Join(rootDir, id)
	kind := NormalizeKind(opts.Kind)
	nowMS := time.Now().UTC().UnixMilli()
	if opts.Lazy {
		return &Session{
			ID:           id,
			Dir:          dir,
			Alias:        opts.Alias,
			Kind:         kind,
			Active:       opts.Active,
			historyPath:  opts.HistoryPath,
			startedAtMS:  nowMS,
			lastActiveMS: nowMS,
			transcript:   newEmptyTranscriptIndex(),
			eventCatalog: opts.EventCatalog,
		}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := ensureScratchpadDir(dir); err != nil {
		return nil, err
	}
	if err := saveMetadata(dir, metadata{
		Alias:          opts.Alias,
		Kind:           kind,
		StartedAtMS:    nowMS,
		LastActiveAtMS: nowMS,
	}); err != nil {
		return nil, err
	}
	convFD, err := openJournalForAppend(filepath.Join(dir, conversationFile), true)
	if err != nil {
		return nil, err
	}
	eventFD, err := openJournalForAppend(filepath.Join(dir, eventsFile), true)
	if err != nil {
		convFD.Close()
		return nil, err
	}
	return &Session{
		ID:           id,
		Dir:          dir,
		Alias:        opts.Alias,
		Kind:         kind,
		Active:       opts.Active,
		convFD:       convFD,
		eventFD:      eventFD,
		historyPath:  opts.HistoryPath,
		startedAtMS:  nowMS,
		lastActiveMS: nowMS,
		transcript:   newEmptyTranscriptIndex(),
		eventCatalog: opts.EventCatalog,
	}, nil
}

// Append adds m to the in-memory history and writes it to conversation.jsonl.
func (s *Session) Append(m llm.Message) error {
	_, err := s.AppendAssigned(m)
	return err
}

// AppendAssigned adds m and returns the normalized message that was persisted.
func (s *Session) AppendAssigned(m llm.Message) (llm.Message, error) {
	messages, err := s.AppendBatchAssigned([]llm.Message{m})
	if len(messages) == 0 {
		return llm.Message{}, err
	}
	return messages[0], err
}

// AppendBatch atomically adds messages to the conversation transcript. A
// failed encode or write leaves both the file and in-memory indexes unchanged.
func (s *Session) AppendBatch(messages []llm.Message) error {
	_, err := s.AppendBatchAssigned(messages)
	return err
}

// AppendBatchAssigned appends a batch and returns the normalized messages,
// including generated IDs, in their persisted order.
func (s *Session) AppendBatchAssigned(messages []llm.Message) ([]llm.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("session: closed")
	}
	if s.journalErr != nil {
		err := s.journalErr
		s.mu.Unlock()
		return nil, err
	}
	prepared := make([]llm.Message, len(messages))
	lines := make([][]byte, len(messages))
	var data []byte
	for i, message := range messages {
		prepared[i] = prepareNewMessage(message)
		sequence := s.transcript.lastSequence + uint64(i) + 1
		line, err := marshalTranscriptJournalLine(s.ID, sequence, prepared[i])
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		lines[i] = line
		data = append(data, line...)
	}
	if err := s.ensureFilesLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	appendGuard, err := acquireLockGuard(filepath.Join(s.Dir, transcriptAppendLock))
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	defer func() { _ = appendGuard.Close() }()
	currentFingerprint, err := s.currentTranscriptFingerprintLocked()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !residentTranscriptFingerprintMatches(s.transcript.fingerprint, currentFingerprint) {
		s.mu.Unlock()
		return nil, ErrTranscriptChanged
	}
	offset, err := s.convFD.Seek(0, io.SeekEnd)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if offset != currentFingerprint.Size {
		s.mu.Unlock()
		return nil, ErrTranscriptChanged
	}
	if s.beforeTranscriptWrite != nil {
		s.beforeTranscriptWrite()
	}
	preWriteFingerprint, err := s.currentTranscriptFingerprintLocked()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if preWriteFingerprint != currentFingerprint || preWriteFingerprint.Size != offset {
		s.mu.Unlock()
		return nil, ErrTranscriptChanged
	}
	if !s.transcript.contentDigestValid {
		s.mu.Unlock()
		return nil, ErrTranscriptChanged
	}
	prefixDigest, appendedDigest, err := digestTranscriptAppend(s.convFD, offset, data)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if prefixDigest != s.transcript.contentDigest {
		s.mu.Unlock()
		return nil, ErrTranscriptChanged
	}
	if s.afterTranscriptPrewriteCheck != nil {
		s.afterTranscriptPrewriteCheck()
	}
	conversationPath := filepath.Join(s.Dir, conversationFile)
	writeErr := appendJournalBytesDurably(s.convFD, conversationPath, offset, data, s.journalOps)
	if writeErr != nil {
		s.recordJournalFailureLocked(writeErr)
		s.mu.Unlock()
		return nil, writeErr
	}
	if s.convFD != nil {
		_ = s.convFD.Close()
		s.convFD = nil
	}
	committedFingerprint, _ := fingerprintFromPath(filepath.Join(s.Dir, conversationFile))
	if s.afterTranscriptWrite != nil {
		s.afterTranscriptWrite()
	}
	nextTranscript := s.transcript
	nextTranscript.repairPending = append([]pendingTranscriptToolUse(nil), s.transcript.repairPending...)
	nextHistory := append(s.History, prepared...)
	entryOffset := offset
	for i, message := range prepared {
		sequence := s.transcript.lastSequence + uint64(i) + 1
		nextTranscript.appendMessage(message, entryOffset, len(lines[i]), sequence)
		entryOffset += int64(len(lines[i]))
	}
	lastActiveMS := time.Now().UTC().UnixMilli()
	if lastActiveMS < s.lastActiveMS {
		lastActiveMS = s.lastActiveMS
	}
	commit, inspectErr := s.inspectTranscriptCommitLocked(
		offset,
		data,
		committedFingerprint,
		prefixDigest,
		appendedDigest,
	)
	if commit.diverged {
		s.adoptTranscriptCommitLocked(commit, nextTranscript, nextHistory, lastActiveMS)
		historyPath, historyInfo, refreshHistory := s.prepareHistoryRefreshLocked()
		_ = appendGuard.Close()
		appendGuard = nil
		s.mu.Unlock()
		if refreshHistory {
			_ = s.finishHistoryRefresh(historyPath, historyInfo)
		}
		if commit.committed {
			return prepared, nil
		}
		if inspectErr != nil {
			return nil, inspectErr
		}
		return nil, ErrTranscriptChanged
	}
	if inspectErr != nil {
		s.mu.Unlock()
		return nil, inspectErr
	}
	nextTranscript.fingerprint = commit.fingerprint
	nextTranscript.contentDigest = commit.contentDigest
	nextTranscript.contentDigestValid = commit.contentDigestValid
	meta := s.metadataLocked()
	meta.LastActiveAtMS = lastActiveMS
	meta.Transcript = buildTranscriptCheckpoint(nextTranscript, nextTranscript.fingerprint)
	if err := saveMetadata(s.Dir, meta); err != nil {
		rollbackErr := s.rollbackConversationLocked(offset)
		s.metadataDirty = rollbackErr == nil
		s.mu.Unlock()
		if rollbackErr != nil {
			return nil, errors.Join(err, fmt.Errorf("session: rollback conversation batch: %w", rollbackErr))
		}
		return nil, err
	}
	s.lastActiveMS = lastActiveMS
	s.History = nextHistory
	s.transcript = activeTranscriptIndex(nextTranscript)
	s.metadataDirty = false
	s.reopenConversationLocked()
	historyPath, historyInfo, refreshHistory := s.prepareHistoryRefreshLocked()
	_ = appendGuard.Close()
	appendGuard = nil
	s.mu.Unlock()
	if refreshHistory {
		_ = s.finishHistoryRefresh(historyPath, historyInfo)
	}
	return prepared, nil
}

type transcriptCommit struct {
	committed          bool
	diverged           bool
	fingerprint        transcriptFingerprint
	index              transcriptIndex
	history            []llm.Message
	contentDigest      transcriptPrefixDigest
	contentDigestValid bool
}

func (s *Session) inspectTranscriptCommitLocked(
	offset int64,
	data []byte,
	committedFingerprint transcriptFingerprint,
	prefixDigest transcriptPrefixDigest,
	appendedDigest transcriptPrefixDigest,
) (transcriptCommit, error) {
	path := filepath.Join(s.Dir, conversationFile)
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		return transcriptCommit{diverged: true}, err
	}
	defer snapshot.close()
	committed, readErr := transcriptRangeMatches(snapshot.file, offset, data)
	expectedSize := offset + int64(len(data))
	incrementalSafe := committedFingerprint.strong() && snapshot.fingerprint == committedFingerprint
	if committed && snapshot.fingerprint.Size == expectedSize {
		incrementalSafe, err = transcriptPrefixDigestMatches(snapshot.file, offset, prefixDigest)
		if err != nil {
			return transcriptCommit{diverged: true}, err
		}
	}
	if incrementalSafe && committed && snapshot.fingerprint.Size == expectedSize {
		if err := snapshot.verify(); err != nil {
			return transcriptCommit{diverged: true}, err
		}
		committed, err = transcriptRangeMatches(snapshot.file, offset, data)
		if err != nil {
			return transcriptCommit{diverged: true}, err
		}
		if !committed {
			return s.inspectDivergedTranscriptCommitLocked(snapshot, offset, data)
		}
		prefixMatches, err := transcriptPrefixDigestMatches(snapshot.file, offset, prefixDigest)
		if err != nil {
			return transcriptCommit{diverged: true}, err
		}
		if !prefixMatches {
			return s.inspectDivergedTranscriptCommitLocked(snapshot, offset, data)
		}
		commit := transcriptCommit{committed: true, fingerprint: snapshot.fingerprint}
		commit.contentDigest = appendedDigest
		commit.contentDigestValid = true
		return commit, nil
	}

	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return transcriptCommit{committed: committed, diverged: true, fingerprint: snapshot.fingerprint}, readErr
	}
	return s.inspectDivergedTranscriptCommitLocked(snapshot, offset, data)
}

type transcriptPrefixDigest [sha256.Size]byte

func digestTranscriptPrefix(file *os.File, size int64) (transcriptPrefixDigest, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, size)); err != nil {
		return transcriptPrefixDigest{}, err
	}
	var digest transcriptPrefixDigest
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func digestTranscriptAppend(
	file *os.File,
	size int64,
	suffix []byte,
) (transcriptPrefixDigest, transcriptPrefixDigest, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, size)); err != nil {
		return transcriptPrefixDigest{}, transcriptPrefixDigest{}, err
	}
	var prefix transcriptPrefixDigest
	copy(prefix[:], hash.Sum(nil))
	if _, err := hash.Write(suffix); err != nil {
		return transcriptPrefixDigest{}, transcriptPrefixDigest{}, err
	}
	var appended transcriptPrefixDigest
	copy(appended[:], hash.Sum(nil))
	return prefix, appended, nil
}

func transcriptPrefixDigestMatches(file *os.File, size int64, expected transcriptPrefixDigest) (bool, error) {
	actual, err := digestTranscriptPrefix(file, size)
	return actual == expected, err
}

func (s *Session) inspectDivergedTranscriptCommitLocked(
	snapshot *transcriptSnapshot,
	offset int64,
	data []byte,
) (transcriptCommit, error) {
	path := filepath.Join(s.Dir, conversationFile)
	result := transcriptCommit{diverged: true, fingerprint: snapshot.fingerprint}
	idx, err := scanTranscriptIndexFromFile(snapshot.file, path, 0)
	if err != nil {
		return result, errors.Join(ErrTranscriptChanged, err)
	}
	idx.fingerprint = snapshot.fingerprint
	active := activeTranscriptIndex(idx)
	history, err := readTranscriptMessagesFromFile(snapshot.file, path, idx.entries)
	if err != nil {
		return result, err
	}
	if err := snapshot.verify(); err != nil {
		return transcriptCommit{diverged: true}, err
	}
	matched, err := transcriptPrefixDigestMatches(snapshot.file, snapshot.fingerprint.Size, idx.contentDigest)
	if err != nil {
		return result, err
	}
	if !idx.contentDigestValid || !matched {
		return result, ErrTranscriptChanged
	}
	result.committed, err = transcriptBatchPresent(snapshot.file, idx.entries, offset, data)
	if err != nil {
		return result, err
	}
	if err := snapshot.verify(); err != nil {
		return transcriptCommit{diverged: true}, err
	}
	matched, err = transcriptPrefixDigestMatches(snapshot.file, snapshot.fingerprint.Size, idx.contentDigest)
	if err != nil || !matched {
		if err != nil {
			return result, err
		}
		return result, ErrTranscriptChanged
	}
	result.index = active
	result.history = history
	result.contentDigest = idx.contentDigest
	result.contentDigestValid = idx.contentDigestValid
	return result, nil
}

func transcriptBatchPresent(file *os.File, entries []transcriptIndexEntry, offset int64, data []byte) (bool, error) {
	for _, entry := range entries {
		if entry.Offset < offset {
			continue
		}
		matched, err := transcriptRangeMatches(file, entry.Offset, data)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func transcriptRangeMatches(file *os.File, offset int64, data []byte) (bool, error) {
	written := make([]byte, len(data))
	n, err := file.ReadAt(written, offset)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return false, err
	}
	return n == len(data) && bytes.Equal(written, data), err
}

func (s *Session) adoptTranscriptCommitLocked(
	commit transcriptCommit,
	fallbackIndex transcriptIndex,
	fallbackHistory []llm.Message,
	lastActiveMS int64,
) {
	if commit.index.fingerprint != (transcriptFingerprint{}) {
		fallbackIndex = commit.index
		fallbackHistory = commit.history
		if !commit.committed {
			lastActiveMS = s.lastActiveMS
		}
	} else if commit.committed {
		fallbackIndex.fingerprint = transcriptFingerprint{}
	} else {
		fallbackIndex = s.transcript
		fallbackIndex.fingerprint = transcriptFingerprint{}
		fallbackHistory = s.History
		lastActiveMS = s.lastActiveMS
	}
	s.reopenConversationLocked()
	s.lastActiveMS = lastActiveMS
	s.History = fallbackHistory
	s.transcript = fallbackIndex
	_ = s.persistMetadataLocked()
	s.historyDirty = s.historyPath != ""
}

func (s *Session) reopenConversationLocked() {
	if s.convFD != nil {
		_ = s.convFD.Close()
		s.convFD = nil
	}
	if replacement, err := openJournalForAppend(filepath.Join(s.Dir, conversationFile), false); err == nil {
		s.convFD = replacement
	}
}

func newEmptyTranscriptIndex() transcriptIndex {
	return transcriptIndex{
		repairSafe:         true,
		repairPrefixSafe:   true,
		complete:           true,
		contentDigest:      sha256.Sum256(nil),
		contentDigestValid: true,
	}
}

// AppendEvent persists e to events.jsonl. Unlike Append, the event itself
// is not retained in memory.
func (s *Session) AppendEvent(e events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("session: closed")
	}
	if s.journalErr != nil {
		return s.journalErr
	}
	if e.Transient {
		return nil
	}
	// Event journaling is canonical in its own right. Retry transcript-derived
	// metadata opportunistically, but never drop an event because that retry failed.
	_ = s.retryDerivedStateLocked()
	if err := s.ensureEventFileLocked(); err != nil {
		return err
	}
	e = events.Normalize(e)
	sequence := s.eventSequence + 1
	line, err := marshalEventJournalLine(s.ID, sequence, e)
	if err != nil {
		return err
	}
	guard, err := acquireEventJournalLock(s.Dir)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()
	offset, err := s.eventFD.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, eventsFile)
	if err := appendJournalBytesDurably(s.eventFD, path, offset, line, s.journalOps); err != nil {
		s.recordJournalFailureLocked(err)
		return err
	}
	s.eventSequence = sequence
	return nil
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.close()
	})
	return s.closeErr
}

func (s *Session) close() error {
	s.mu.Lock()
	s.closed = true
	closeErr := s.journalErr
	if s.metadataDirty {
		if err := s.persistMetadataLocked(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	historyPath, historyInfo, refreshHistory := "", Info{}, false
	if s.historyDirty {
		historyPath, historyInfo, refreshHistory = s.prepareHistoryRefreshLocked()
	}
	if s.convFD != nil {
		if err := completeJournalFileOps(s.journalOps).sync(s.convFD); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("session: sync conversation journal on close: %w", err))
		}
		if err := s.convFD.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		s.convFD = nil
	}
	if s.eventFD != nil {
		if err := completeJournalFileOps(s.journalOps).sync(s.eventFD); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("session: sync events journal on close: %w", err))
		}
		if err := s.eventFD.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		s.eventFD = nil
	}
	s.mu.Unlock()
	if refreshHistory {
		if err := s.finishHistoryRefresh(historyPath, historyInfo); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

func (s *Session) recordJournalFailureLocked(err error) {
	var rollbackErr *journalRollbackError
	if !errors.As(err, &rollbackErr) {
		return
	}
	if s.journalErr == nil {
		s.journalErr = fmt.Errorf("session: journal unavailable after failed rollback: %w", err)
	}
}

// Load reads conversation.jsonl from dir and returns the assembled session.
// The new session shares the same id (= directory basename) and appends to
// the existing files.
func Load(dir string) (*Session, error) {
	return LoadWithOptions(dir, Options{})
}

func LoadWithOptions(dir string, opts Options) (*Session, error) {
	if opts.RepairTranscript && opts.EventCatalog == nil {
		return nil, errors.New("session: transcript repair requires an event catalog")
	}
	id := filepath.Base(dir)
	meta, err := loadMetadata(dir)
	if err != nil {
		return nil, err
	}
	alias := meta.Alias
	kind := meta.Kind
	convPath := filepath.Join(dir, conversationFile)
	var idx transcriptIndex
	loadedFullTranscript := false
	if opts.RepairTranscript {
		if meta.Transcript == nil || !meta.Transcript.RepairSafe {
			idx, err = scanTranscriptIndex(convPath)
			loadedFullTranscript = true
		} else {
			var checkpointed bool
			idx, checkpointed, err = loadActiveTranscriptIndex(convPath, meta.Transcript)
			if err == nil && !checkpointed {
				if idx.complete {
					loadedFullTranscript = true
				} else {
					idx, err = scanTranscriptIndex(convPath)
					loadedFullTranscript = true
				}
			}
		}
	} else {
		idx, _, err = loadActiveTranscriptIndex(convPath, meta.Transcript)
	}
	if err != nil {
		return nil, err
	}
	history, err := readTranscriptMessagesForFingerprint(convPath, idx.entries, idx.fingerprint)
	if err != nil {
		return nil, err
	}
	if err := ensureScratchpadDir(dir); err != nil {
		return nil, err
	}
	convFD, err := openJournalForAppend(convPath, false)
	if err != nil {
		return nil, err
	}
	var eventJournal []events.Event
	eventSequence, err := replayEventJournalWithCatalog(dir, opts.EventCatalog, func(event events.Event) {
		eventJournal = append(eventJournal, event)
	})
	if err != nil {
		convFD.Close()
		return nil, err
	}
	eventFD, err := openJournalForAppend(filepath.Join(dir, eventsFile), true)
	if err != nil {
		convFD.Close()
		return nil, err
	}
	tokenUsage, contextUsage, _ := loadLatestSessionUsage(dir)
	sess := &Session{
		ID:            id,
		Dir:           dir,
		Alias:         alias,
		Kind:          kind,
		Active:        opts.Active,
		History:       history,
		TokenUsage:    tokenUsage,
		ContextUsage:  contextUsage,
		convFD:        convFD,
		eventFD:       eventFD,
		transcript:    idx,
		historyPath:   opts.HistoryPath,
		startedAtMS:   meta.StartedAtMS,
		lastActiveMS:  meta.LastActiveAtMS,
		eventSequence: eventSequence,
		eventCatalog:  opts.EventCatalog,
	}
	if opts.RepairTranscript {
		repairs, err := sess.repairTranscriptWithEvents("load", eventJournal)
		if err != nil {
			_ = sess.Close()
			return nil, err
		}
		if loadedFullTranscript && len(repairs) == 0 {
			activeIndex := activeTranscriptIndex(sess.transcript)
			activeHistory, err := readTranscriptMessagesForFingerprint(
				convPath, activeIndex.entries, activeIndex.fingerprint,
			)
			if err != nil {
				_ = sess.Close()
				return nil, err
			}
			sess.transcript = activeIndex
			sess.History = activeHistory
		}
		if len(repairs) > 0 {
			if err := sess.appendTranscriptRepairEvents(opts.EventCatalog, "load", repairs); err != nil {
				_ = sess.Close()
				return nil, err
			}
		}
	}
	return sess, nil
}

// SubscribeBus installs this Session as the bus commit boundary. App runtime
// wiring uses events.DurableSink for additional projections and deliveries.
func (s *Session) SubscribeBus(bus *events.Bus) func() {
	if bus == nil {
		return func() {}
	}
	bus.SetCommitter(sessionEventCommitter{session: s})
	return func() { bus.SetCommitter(nil) }
}

type sessionEventCommitter struct {
	session *Session
}

func (c sessionEventCommitter) Commit(event events.Event) (events.Event, error) {
	event = events.Normalize(event)
	if c.session == nil || event.Transient {
		return event, nil
	}
	if err := c.session.AppendEvent(event); err != nil {
		return events.Event{}, err
	}
	return event, nil
}

// Info returns a summary of the in-memory session.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

// ApplyAlias updates an owned Session after its process-level session lock has
// been acquired. Persisted sessions reload metadata first so concurrent work
// completed before lock acquisition cannot be overwritten by stale times.
func (s *Session) ApplyAlias(alias string) error {
	if alias == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.retryDerivedStateLocked(); err != nil {
		return err
	}
	if s.convFD == nil {
		s.Alias = alias
		return nil
	}
	meta, err := loadMetadata(s.Dir)
	if err != nil {
		return err
	}
	meta.Alias = alias
	if err := saveMetadata(s.Dir, meta); err != nil {
		return err
	}
	s.Alias = alias
	s.startedAtMS = meta.StartedAtMS
	s.lastActiveMS = meta.LastActiveAtMS
	return nil
}

func (s *Session) RecordResponseUsage(usage llm.Usage, contextUsage *llm.ContextUsage) llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !usage.IsZero() {
		s.TokenUsage.Add(usage)
	}
	if contextUsage != nil {
		s.ContextUsage = contextUsage
	}
	return s.TokenUsage
}

func (s *Session) TokenUsageSnapshot() llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TokenUsage
}

func (s *Session) ContextUsageSnapshot() *llm.ContextUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ContextUsage == nil {
		return nil
	}
	usage := *s.ContextUsage
	usage.Breakdown = append([]llm.ContextUsagePart(nil), s.ContextUsage.Breakdown...)
	return &usage
}

// Snapshot returns the current summary and a copy of the in-memory history.
func (s *Session) Snapshot() (Info, []llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := append([]llm.Message(nil), s.History...)
	return s.infoLocked(), msgs
}

func (s *Session) ensureFilesLocked() error {
	if err := s.retryDerivedStateLocked(); err != nil {
		return err
	}
	if s.convFD != nil && s.eventFD != nil {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if err := ensureScratchpadDir(s.Dir); err != nil {
		return err
	}
	if err := saveMetadata(s.Dir, s.metadataLocked()); err != nil {
		return err
	}
	if s.convFD == nil {
		convFD, err := openJournalForAppend(filepath.Join(s.Dir, conversationFile), true)
		if err != nil {
			return err
		}
		s.convFD = convFD
	}
	if s.eventFD == nil {
		eventFD, err := openJournalForAppend(filepath.Join(s.Dir, eventsFile), true)
		if err != nil {
			return err
		}
		s.eventFD = eventFD
	}
	return nil
}

func (s *Session) ensureEventFileLocked() error {
	if s.eventFD != nil {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if err := ensureScratchpadDir(s.Dir); err != nil {
		return err
	}
	metadataPath := filepath.Join(s.Dir, metadataFile)
	if _, err := os.Stat(metadataPath); errors.Is(err, os.ErrNotExist) && !s.metadataDirty {
		if err := saveMetadata(s.Dir, s.metadataLocked()); err != nil {
			return err
		}
		if s.convFD == nil {
			conversationPath := filepath.Join(s.Dir, conversationFile)
			conversation, err := openJournalForAppend(conversationPath, true)
			if err != nil {
				return err
			}
			s.convFD = conversation
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	eventFD, err := openJournalForAppend(filepath.Join(s.Dir, eventsFile), true)
	if err != nil {
		return err
	}
	s.eventFD = eventFD
	return nil
}

func (s *Session) retryDerivedStateLocked() error {
	if s.metadataDirty {
		if err := s.persistMetadataLocked(); err != nil {
			return fmt.Errorf("session: retry derived metadata: %w", err)
		}
	}
	return nil
}

func (s *Session) persistMetadataLocked() error {
	err := saveMetadata(s.Dir, s.metadataLocked())
	s.metadataDirty = err != nil
	return err
}

func (s *Session) prepareHistoryRefreshLocked() (string, Info, bool) {
	if s.historyPath == "" {
		s.historyDirty = false
		return "", Info{}, false
	}
	s.historyDirty = true
	return s.historyPath, s.infoLocked(), true
}

func (s *Session) finishHistoryRefresh(path string, info Info) error {
	err := RecordSession(path, info)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.historyDirty = true
		return err
	}
	current := s.infoLocked()
	if current.transcript == info.transcript &&
		current.transcriptDigestValid == info.transcriptDigestValid &&
		current.transcriptDigest == info.transcriptDigest &&
		current.Turns == info.Turns && current.Preview == info.Preview {
		s.historyDirty = false
		return nil
	}
	s.historyDirty = true
	return nil
}

func prepareNewMessage(m llm.Message) llm.Message {
	m = llm.ClassifyUserMessage(m)
	if m.ID == "" {
		m.ID = newMessageID()
	}
	if m.Blocks == nil {
		m.Blocks = []llm.Block{}
	}
	return m
}

func normalizeLoadedMessage(path string, line int, m llm.Message) (llm.Message, error) {
	if m.ID == "" {
		return llm.Message{}, fmt.Errorf(
			"session: load %s:%d: message id is empty; manually add a unique non-empty \"id\" to this JSONL object; related structural repair code is in internal/session/transcript_repair.go",
			path,
			line,
		)
	}
	if m.Blocks == nil {
		m.Blocks = []llm.Block{}
	}
	m = llm.ClassifyUserMessage(m)
	return m, nil
}

func (s *Session) infoLocked() Info {
	info := Info{
		ID:           s.ID,
		Alias:        s.Alias,
		Dir:          s.Dir,
		Kind:         s.Kind,
		Active:       s.Active,
		StartedAt:    time.UnixMilli(s.startedAtMS).UTC(),
		LastActiveAt: time.UnixMilli(s.lastActiveMS).UTC(),
	}
	info.transcript = s.transcript.fingerprint
	info.transcriptDigest = s.transcript.contentDigest
	info.transcriptDigestValid = s.transcript.contentDigestValid
	if info.transcript == (transcriptFingerprint{}) {
		if s.convFD != nil {
			info.transcript, _ = fingerprintFromOpenFile(s.convFD)
		} else {
			info.transcript, _ = fingerprintFromPath(filepath.Join(s.Dir, conversationFile))
		}
	}
	if len(s.transcript.entries) > 0 || len(s.History) == 0 {
		info.Turns = s.transcript.turns
		info.Preview = s.transcript.preview
	} else {
		for _, m := range s.History {
			if m.Role == llm.RoleUser && m.Kind != llm.MessageKindCompact {
				info.Turns++
				if info.Preview == "" {
					info.Preview = truncateRunes(strings.TrimSpace(m.FirstText()), previewMaxRunes)
				}
			}
		}
	}
	info.TokenUsage = s.TokenUsage
	info.ContextUsage = s.ContextUsage
	return info
}

func (s *Session) metadataLocked() metadata {
	meta := metadata{
		Alias:          s.Alias,
		Kind:           s.Kind,
		StartedAtMS:    s.startedAtMS,
		LastActiveAtMS: s.lastActiveMS,
	}
	meta.Transcript = buildTranscriptCheckpoint(s.transcript, s.transcript.fingerprint)
	return meta
}

func (s *Session) rollbackConversationLocked(offset int64) error {
	path := filepath.Join(s.Dir, conversationFile)
	rollbackErr := repairTornJournalTail(path, offset, s.journalOps)
	if rollbackErr == nil {
		fingerprint, err := fingerprintFromPath(filepath.Join(s.Dir, conversationFile))
		if err != nil {
			return err
		}
		s.transcript.fingerprint = fingerprint
	}
	return rollbackErr
}

func (s *Session) currentTranscriptFingerprintLocked() (transcriptFingerprint, error) {
	if s.convFD == nil {
		return transcriptFingerprint{}, fmt.Errorf("session: conversation file closed")
	}
	openFingerprint, err := fingerprintFromOpenFile(s.convFD)
	if err != nil {
		return transcriptFingerprint{}, err
	}
	pathFingerprint, err := fingerprintFromPath(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		return transcriptFingerprint{}, err
	}
	if openFingerprint != pathFingerprint {
		return transcriptFingerprint{}, ErrTranscriptChanged
	}
	return openFingerprint, nil
}

func residentTranscriptFingerprintMatches(resident, current transcriptFingerprint) bool {
	if resident == (transcriptFingerprint{}) {
		return current.Size == 0
	}
	return resident == current
}

func transcriptDigestMatchesPath(
	path string,
	fingerprint transcriptFingerprint,
	digest transcriptPrefixDigest,
	valid bool,
) (bool, error) {
	if !valid {
		return false, nil
	}
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		return false, err
	}
	defer snapshot.close()
	if snapshot.fingerprint != fingerprint {
		return false, nil
	}
	matched, err := transcriptPrefixDigestMatches(snapshot.file, fingerprint.Size, digest)
	if err != nil || !matched {
		return false, err
	}
	if err := snapshot.verify(); err != nil {
		return false, nil
	}
	return transcriptPrefixDigestMatches(snapshot.file, fingerprint.Size, digest)
}

func newID() string {
	var b [4]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(fmt.Errorf("session: random id bytes: %w", err))
	}
	return time.Now().UTC().Format(idTimeLayout) + "-" + hex.EncodeToString(b[:])
}

// ValidID reports whether id has the path-safe shape produced by newID. The
// timestamp-like prefix is cosmetic and does not need to represent a real date.
func ValidID(id string) bool {
	const suffixBytes = 4
	timestampLength := len(idTimeLayout)
	if len(id) != timestampLength+1+hex.EncodedLen(suffixBytes) ||
		id[timestampLength] != '-' {
		return false
	}
	for i, c := range id[:timestampLength] {
		if i == 8 {
			if c != 'T' {
				return false
			}
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	suffix := id[timestampLength+1:]
	decoded, err := hex.DecodeString(suffix)
	return err == nil && hex.EncodeToString(decoded) == suffix
}

func idCreatedAt(id string) (time.Time, bool) {
	const suffixBytes = 4
	timestampLength := len(idTimeLayout)
	if len(id) != timestampLength+1+hex.EncodedLen(suffixBytes) ||
		id[timestampLength] != '-' {
		return time.Time{}, false
	}
	timestamp := id[:timestampLength]
	createdAt, err := time.Parse(idTimeLayout, timestamp)
	if err != nil {
		return time.Time{}, false
	}
	suffix := id[timestampLength+1:]
	decoded, err := hex.DecodeString(suffix)
	if err != nil || hex.EncodeToString(decoded) != suffix {
		return time.Time{}, false
	}
	return createdAt, true
}

// MessageCreatedAt extracts the creation time encoded in a canonical message
// ID. Caller-supplied message IDs do not carry this metadata.
func MessageCreatedAt(id string) (time.Time, bool) {
	const prefix = "msg-"
	if !strings.HasPrefix(id, prefix) {
		return time.Time{}, false
	}
	return idCreatedAt(strings.TrimPrefix(id, prefix))
}

func newMessageID() string {
	return "msg-" + newID()
}
