package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

const (
	pendingInputSummaryLength   = 200
	pendingInputDocumentVersion = 1
	pendingInputFile            = "pending_inputs.json"
)

var ErrCorruptPendingInputs = errors.New("runtime: corrupt pending input state")

type PendingInputState string

const (
	PendingInputStateAccepting    PendingInputState = "accepting"
	PendingInputStatePending      PendingInputState = "pending"
	PendingInputStateAdmitted     PendingInputState = "admitted"
	PendingInputStateProcessed    PendingInputState = "processed"
	PendingInputStateRetryable    PendingInputState = "retryable"
	PendingInputStateDeadLettered PendingInputState = "dead_lettered"
	// Expired and dropped are transient dispositions. They are never retained
	// in the bounded pending document.
	PendingInputStateExpired PendingInputState = "expired"
	PendingInputStateDropped PendingInputState = "dropped"
)

type PendingInputOrigin string

const (
	PendingInputOriginQueued PendingInputOrigin = "queued"
	PendingInputOriginTurn   PendingInputOrigin = "turn"
)

type PendingInputOptions struct {
	ID  string
	TTL time.Duration
}

type PendingInputQueueOptions struct {
	Now       func() time.Time
	Thread    *thread.Thread
	WriteFile func(string, []byte, os.FileMode, os.FileMode) error
}

// PendingInputRecoveryFacts close admission and transcript crash windows that
// do not yet have a terminal Generation event.
type PendingInputRecoveryFacts struct {
	AdmittedMessageIDs   map[string]struct{}
	TranscriptMessageIDs map[string]struct{}
}

type PendingInputRecord struct {
	ID          string             `json:"id"`
	TurnID      string             `json:"turn_id,omitempty"`
	MessageID   string             `json:"message_id"`
	Message     llm.Message        `json:"message"`
	Summary     string             `json:"summary,omitempty"`
	Origin      PendingInputOrigin `json:"origin,omitempty"`
	State       PendingInputState  `json:"state"`
	CreatedAt   time.Time          `json:"created_at"`
	ExpiresAt   time.Time          `json:"expires_at,omitempty"`
	Attempts    int                `json:"attempts"`
	ProcessedAt *time.Time         `json:"processed_at,omitempty"`
	LastError   string             `json:"last_error,omitempty"`
}

func (r PendingInputRecord) Expired(now time.Time) bool {
	return r.Origin != PendingInputOriginTurn && isReplayablePendingState(r.State) &&
		!r.ExpiresAt.IsZero() && !r.ExpiresAt.After(now)
}

type pendingInputDocument struct {
	Version int                  `json:"v"`
	Records []PendingInputRecord `json:"records"`
}

type pendingTerminalDisposition uint8

const (
	pendingTerminalNone pendingTerminalDisposition = iota
	pendingTerminalCompleted
	pendingTerminalCancelled
	pendingTerminalRetryable
	pendingTerminalDeadLettered
)

type PendingInputQueue struct {
	thread       *thread.Thread
	dir          string
	now          func() time.Time
	writeFile    func(string, []byte, os.FileMode, os.FileMode) error
	mu           sync.Mutex
	loaded       bool
	records      map[string]PendingInputRecord
	order        []string
	messageIndex map[string]string
	turnIndex    map[string]string
	completed    map[string]struct{}
	handled      map[string]struct{}
	handledMsgs  map[string]struct{}
}

func NewPendingInputQueue(dir string, opts PendingInputQueueOptions) *PendingInputQueue {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	writeFile := opts.WriteFile
	if writeFile == nil {
		writeFile = homestore.WriteFileAtomic
	}
	if opts.Thread != nil {
		dir = opts.Thread.Dir
	}
	return &PendingInputQueue{thread: opts.Thread, dir: filepath.Clean(dir), now: now, writeFile: writeFile}
}

func (q *PendingInputQueue) Enqueue(msg llm.Message, opts PendingInputOptions, turnID string) (PendingInputRecord, error) {
	if q == nil {
		return PendingInputRecord{}, fmt.Errorf("pending input queue: nil store")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return PendingInputRecord{}, err
	}
	id := strings.TrimSpace(opts.ID)
	if id != "" {
		if existing, ok := q.records[id]; ok {
			return existing, nil
		}
		if _, completed := q.completed[id]; completed {
			return PendingInputRecord{}, ErrPendingInputHandled
		}
		if _, handled := q.handled[id]; handled {
			return PendingInputRecord{}, ErrPendingInputHandled
		}
	} else {
		id = nextUniquePendingInputID(q.records, newPendingInputID)
	}
	now := q.nowMillis()
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultPendingInputTTL
	}
	msg.ID = pendingInputMessageID(id, now)
	if msg.Blocks == nil {
		msg.Blocks = []llm.Block{}
	}
	record := PendingInputRecord{
		ID: id, TurnID: turnID, MessageID: msg.ID, Message: msg,
		Summary: truncate(msg.FirstText(), pendingInputSummaryLength),
		Origin:  PendingInputOriginQueued, State: PendingInputStatePending,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := q.upsertLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	return record, nil
}

func (q *PendingInputQueue) AdmitTurnInput(turnID string, msg llm.Message, reuseTurnRecord bool) (PendingInputRecord, error) {
	return q.storeTurnInput(turnID, msg, reuseTurnRecord, PendingInputStateAdmitted)
}

func (q *PendingInputQueue) StageTurnInput(turnID string, msg llm.Message, reuseTurnRecord bool) (PendingInputRecord, error) {
	return q.storeTurnInput(turnID, msg, reuseTurnRecord, PendingInputStateAccepting)
}

func (q *PendingInputQueue) storeTurnInput(turnID string, msg llm.Message, reuseTurnRecord bool, state PendingInputState) (PendingInputRecord, error) {
	if q == nil {
		return PendingInputRecord{}, fmt.Errorf("pending input queue: nil store")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return PendingInputRecord{}, err
	}
	if reuseTurnRecord {
		if id := q.turnIndex[turnID]; id != "" {
			return q.records[id], nil
		}
	}
	if msg.ID != "" {
		if _, handled := q.handledMsgs[msg.ID]; handled {
			return PendingInputRecord{}, ErrPendingInputHandled
		}
		if id := q.messageIndex[msg.ID]; id != "" {
			record := q.records[id]
			if isReplayablePendingState(record.State) {
				if state == PendingInputStateAccepting {
					return record, nil
				}
				return q.promoteTurnInputLocked(record, turnID, state)
			}
			return PendingInputRecord{}, ErrPendingInputHandled
		}
	}

	id := nextUniquePendingInputID(q.records, newPendingInputID)
	now := q.nowMillis()
	msg.ID = pendingInputMessageID(id, now)
	if msg.Blocks == nil {
		msg.Blocks = []llm.Block{}
	}
	attempts := 0
	if state == PendingInputStateAdmitted {
		attempts = 1
	}
	record := PendingInputRecord{
		ID: id, TurnID: turnID, MessageID: msg.ID, Message: msg,
		Summary: truncate(msg.FirstText(), pendingInputSummaryLength),
		Origin:  PendingInputOriginTurn, State: state, CreatedAt: now, Attempts: attempts,
	}
	if err := q.upsertLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	return record, nil
}

func (q *PendingInputQueue) promoteTurnInputLocked(record PendingInputRecord, turnID string, state PendingInputState) (PendingInputRecord, error) {
	if record.Origin == PendingInputOriginTurn && record.State == state && record.TurnID == turnID {
		return record, nil
	}
	record.Origin = PendingInputOriginTurn
	record.State = state
	record.TurnID = turnID
	record.ExpiresAt = time.Time{}
	record.Attempts++
	record.ProcessedAt = nil
	record.LastError = ""
	if err := q.upsertLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	return record, nil
}

func (q *PendingInputQueue) CommitTurnInput(id, turnID string) error {
	if q == nil {
		return fmt.Errorf("pending input queue: nil store")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	record, ok := q.records[id]
	if !ok {
		return fmt.Errorf("pending input queue: admission intent %q not found", id)
	}
	if record.Origin == PendingInputOriginTurn && record.State == PendingInputStateAdmitted && record.TurnID == turnID {
		return nil
	}
	if record.State == PendingInputStateAccepting && record.TurnID != turnID {
		return fmt.Errorf("pending input queue: admission intent %q has state %q for turn %q", id, record.State, record.TurnID)
	}
	if !isReplayablePendingState(record.State) {
		return fmt.Errorf("pending input queue: admission intent %q has state %q for turn %q", id, record.State, record.TurnID)
	}
	wasAccepting := record.State == PendingInputStateAccepting
	previousTurnID := record.TurnID
	record.Origin = PendingInputOriginTurn
	record.State = PendingInputStateAdmitted
	record.TurnID = turnID
	record.ExpiresAt = time.Time{}
	if wasAccepting || record.Attempts == 0 || previousTurnID != turnID {
		record.Attempts++
	}
	record.ProcessedAt = nil
	record.LastError = ""
	return q.upsertLocked(record)
}

func (q *PendingInputQueue) Replayable(turnID string, limit int) ([]PendingInputRecord, error) {
	if q == nil {
		return nil, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	now := q.nowMillis()
	records, order := q.cloneStateLocked()
	changed := false
	for _, id := range append([]string(nil), order...) {
		record := records[id]
		if record.Expired(now) {
			delete(records, id)
			order = removePendingInputID(order, id)
			changed = true
		}
	}
	if changed {
		if err := q.persistLocked(records, order); err != nil {
			return nil, err
		}
	}
	out := make([]PendingInputRecord, 0, len(q.order))
	for _, id := range q.order {
		record := q.records[id]
		if !isReplayablePendingState(record.State) || record.State == PendingInputStateRetryable || record.State == PendingInputStateDeadLettered {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, record)
	}
	return out, nil
}

func (q *PendingInputQueue) MarkAdmitted(ids []string, turnID string) error {
	return q.updateStates(ids, func(record PendingInputRecord, _ time.Time) (PendingInputRecord, bool, bool) {
		if !isReplayablePendingState(record.State) || record.State == PendingInputStateDeadLettered {
			return record, false, false
		}
		if record.State == PendingInputStateAdmitted && record.TurnID == turnID {
			return record, false, false
		}
		record.State = PendingInputStateAdmitted
		record.TurnID = turnID
		record.ExpiresAt = time.Time{}
		record.Attempts++
		record.ProcessedAt = nil
		record.LastError = ""
		return record, true, false
	})
}

func (q *PendingInputQueue) PromoteToTurnInput(ids []string, turnID string) error {
	return q.updateStates(ids, func(record PendingInputRecord, _ time.Time) (PendingInputRecord, bool, bool) {
		if !isReplayablePendingState(record.State) || record.State == PendingInputStateDeadLettered {
			return record, false, false
		}
		if record.Origin == PendingInputOriginTurn && record.State == PendingInputStateAdmitted && record.TurnID == turnID {
			return record, false, false
		}
		record.Origin = PendingInputOriginTurn
		record.State = PendingInputStateAdmitted
		record.TurnID = turnID
		record.ExpiresAt = time.Time{}
		record.Attempts++
		record.ProcessedAt = nil
		record.LastError = ""
		return record, true, false
	})
}

func (q *PendingInputQueue) MarkProcessed(ids []string) error {
	return q.updateStates(ids, func(record PendingInputRecord, now time.Time) (PendingInputRecord, bool, bool) {
		if !isReplayablePendingState(record.State) || record.State == PendingInputStateDeadLettered || record.State == PendingInputStateProcessed {
			return record, false, false
		}
		record.State = PendingInputStateProcessed
		record.ProcessedAt = &now
		return record, true, false
	})
}

func (q *PendingInputQueue) MarkMessageProcessed(messageID string) error {
	if q == nil || messageID == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	id := q.messageIndex[messageID]
	if id == "" {
		return nil
	}
	record := q.records[id]
	if record.State == PendingInputStateProcessed || record.State == PendingInputStateDeadLettered {
		return nil
	}
	now := q.nowMillis()
	record.State = PendingInputStateProcessed
	record.ProcessedAt = &now
	return q.upsertLocked(record)
}

func (q *PendingInputQueue) MarkDropped(ids []string) error { return q.remove(ids) }

func (q *PendingInputQueue) MarkExpired(ids []string) error { return q.remove(ids) }

func (q *PendingInputQueue) Retry(id string) error {
	return q.updateStates([]string{id}, func(record PendingInputRecord, _ time.Time) (PendingInputRecord, bool, bool) {
		if record.State != PendingInputStateDeadLettered && record.State != PendingInputStateRetryable {
			return record, false, false
		}
		record.State = PendingInputStatePending
		record.TurnID = ""
		record.ProcessedAt = nil
		record.LastError = ""
		return record, true, false
	})
}

// RetryTurnInputs returns every runtime-interrupted Input from one consuming
// Turn to pending state in a single document replacement. Fleet uses this
// explicit handoff before admitting its restart continuation.
func (q *PendingInputQueue) RetryTurnInputs(turnID string) (int, error) {
	if q == nil || strings.TrimSpace(turnID) == "" {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return 0, err
	}
	records, order := q.cloneStateLocked()
	retried := 0
	for _, id := range order {
		record := records[id]
		if record.State != PendingInputStateRetryable || record.TurnID != turnID {
			continue
		}
		record.State = PendingInputStatePending
		record.TurnID = ""
		record.ProcessedAt = nil
		record.LastError = ""
		records[id] = record
		retried++
	}
	if retried == 0 {
		return 0, nil
	}
	if err := q.persistLocked(records, order); err != nil {
		return 0, err
	}
	return retried, nil
}

func (q *PendingInputQueue) Cancel(id string) error { return q.remove([]string{id}) }

func (q *PendingInputQueue) Records() (map[string]PendingInputRecord, error) {
	if q == nil {
		return map[string]PendingInputRecord{}, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	return clonePendingInputRecords(q.records), nil
}

func (q *PendingInputQueue) Completed(id string) (bool, error) {
	if q == nil || id == "" {
		return false, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return false, err
	}
	_, ok := q.completed[id]
	return ok, nil
}

func (q *PendingInputQueue) InputIDsForTurn(turnID string) ([]string, error) {
	if q == nil || turnID == "" {
		return nil, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	var ids []string
	for _, id := range q.order {
		record := q.records[id]
		if record.TurnID == turnID && (record.State == PendingInputStateAdmitted || record.State == PendingInputStateProcessed) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (q *PendingInputQueue) ApplyTerminalEvent(event events.Event) error {
	if q == nil {
		return nil
	}
	disposition, ids, message := pendingDispositionFromEvent(event)
	if disposition == pendingTerminalNone || len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	return q.applyTerminalLocked(disposition, ids, message)
}

// ReconcileRecoveryFacts converts interrupted admission/attempt state into a
// replayable state after terminal Generation events have already been applied.
func (q *PendingInputQueue) ReconcileRecoveryFacts(facts PendingInputRecoveryFacts) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	records, order := q.cloneStateLocked()
	changed := false
	for _, id := range order {
		record := records[id]
		if record.State == PendingInputStateDeadLettered {
			continue
		}
		_, transcribed := facts.TranscriptMessageIDs[record.MessageID]
		_, admitted := facts.AdmittedMessageIDs[record.MessageID]
		switch record.State {
		case PendingInputStateAccepting:
			if admitted {
				record.State = PendingInputStateAdmitted
			} else {
				record.State = PendingInputStatePending
				record.Origin = PendingInputOriginQueued
				record.TurnID = ""
			}
			changed = true
		case PendingInputStateAdmitted, PendingInputStateProcessed:
			const interrupted = "process ended before a terminal Turn record"
			if record.LastError != interrupted {
				record.LastError = interrupted
				changed = true
			}
		}
		if transcribed && record.ProcessedAt == nil {
			now := q.nowMillis()
			record.ProcessedAt = &now
			changed = true
		}
		records[id] = record
	}
	if !changed {
		return nil
	}
	return q.persistLocked(records, order)
}

func (q *PendingInputQueue) updateStates(ids []string, update func(PendingInputRecord, time.Time) (PendingInputRecord, bool, bool)) error {
	if q == nil || len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	records, order := q.cloneStateLocked()
	now := q.nowMillis()
	changed := false
	for _, id := range ids {
		record, ok := records[id]
		if !ok {
			continue
		}
		updated, mutate, remove := update(record, now)
		if remove {
			delete(records, id)
			order = removePendingInputID(order, id)
			changed = true
		} else if mutate {
			records[id] = updated
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return q.persistLocked(records, order)
}

func (q *PendingInputQueue) remove(ids []string) error {
	if q == nil || len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	records, order := q.cloneStateLocked()
	removed := make([]PendingInputRecord, 0, len(ids))
	for _, id := range ids {
		record, ok := records[id]
		if !ok {
			continue
		}
		removed = append(removed, record)
		delete(records, id)
		order = removePendingInputID(order, id)
	}
	if len(removed) == 0 {
		return nil
	}
	if err := q.persistLocked(records, order); err != nil {
		return err
	}
	for _, record := range removed {
		q.handled[record.ID] = struct{}{}
		q.handledMsgs[record.MessageID] = struct{}{}
	}
	return nil
}

func (q *PendingInputQueue) ensureLoadedLocked() error {
	if q.loaded {
		return nil
	}
	records, order, err := q.loadDocumentLocked()
	if err != nil {
		return err
	}
	q.records = records
	q.order = order
	q.completed = map[string]struct{}{}
	q.handled = map[string]struct{}{}
	q.handledMsgs = map[string]struct{}{}
	q.rebuildIndexesLocked()
	changed := false
	if q.thread != nil {
		eventsList, readErr := q.thread.ReadEvents()
		if readErr != nil {
			return fmt.Errorf("pending input queue: read Generation events: %w", readErr)
		}
		for _, event := range eventsList {
			disposition, ids, message := pendingDispositionFromEvent(event)
			if disposition != pendingTerminalNone {
				for _, id := range ids {
					q.handled[id] = struct{}{}
				}
			}
			if disposition == pendingTerminalCompleted {
				for _, id := range ids {
					q.completed[id] = struct{}{}
				}
			}
			if q.applyTerminalStateLocked(disposition, ids, message) {
				changed = true
			}
		}
	}
	q.loaded = true
	if changed {
		if err := q.persistCurrentLocked(); err != nil {
			return err
		}
		return q.synchronizePendingCountLocked(true)
	}
	if err := q.synchronizePendingCountLocked(true); err != nil {
		q.loaded = false
		return err
	}
	return nil
}

func (q *PendingInputQueue) loadDocumentLocked() (map[string]PendingInputRecord, []string, error) {
	records := map[string]PendingInputRecord{}
	data, err := os.ReadFile(q.pathLocked())
	if errors.Is(err, os.ErrNotExist) {
		return records, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("pending input queue: read state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document pendingInputDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCorruptPendingInputs, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrCorruptPendingInputs, err)
	}
	if document.Version != pendingInputDocumentVersion {
		return nil, nil, fmt.Errorf("%w: unsupported version %d", ErrCorruptPendingInputs, document.Version)
	}
	order := make([]string, 0, len(document.Records))
	messages := map[string]string{}
	for index, record := range document.Records {
		if err := validatePendingInputRecord(record); err != nil {
			return nil, nil, fmt.Errorf("%w: record %d: %v", ErrCorruptPendingInputs, index, err)
		}
		if _, exists := records[record.ID]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate id %q", ErrCorruptPendingInputs, record.ID)
		}
		if previous := messages[record.MessageID]; previous != "" {
			return nil, nil, fmt.Errorf("%w: message %q belongs to %q and %q", ErrCorruptPendingInputs, record.MessageID, previous, record.ID)
		}
		records[record.ID] = record
		order = append(order, record.ID)
		messages[record.MessageID] = record.ID
	}
	return records, order, nil
}

func validatePendingInputRecord(record PendingInputRecord) error {
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.ID) != record.ID ||
		strings.TrimSpace(record.MessageID) == "" || strings.TrimSpace(record.MessageID) != record.MessageID ||
		record.Message.ID != record.MessageID || record.Message.Role != llm.RoleUser || record.Message.Blocks == nil {
		return errors.New("invalid input or message identity")
	}
	if record.CreatedAt.IsZero() || record.Attempts < 0 {
		return errors.New("invalid creation time or attempt count")
	}
	if record.Origin != PendingInputOriginQueued && record.Origin != PendingInputOriginTurn {
		return fmt.Errorf("invalid origin %q", record.Origin)
	}
	switch record.State {
	case PendingInputStateAccepting:
		if record.Origin != PendingInputOriginTurn || record.TurnID == "" {
			return errors.New("accepting input must identify its Turn")
		}
	case PendingInputStatePending:
	case PendingInputStateAdmitted, PendingInputStateProcessed, PendingInputStateRetryable, PendingInputStateDeadLettered:
		if record.TurnID == "" {
			return fmt.Errorf("%s input must identify its Turn", record.State)
		}
	default:
		return fmt.Errorf("state %q is not persistable", record.State)
	}
	return nil
}

func (q *PendingInputQueue) upsertLocked(record PendingInputRecord) error {
	records, order := q.cloneStateLocked()
	if _, exists := records[record.ID]; !exists {
		order = append(order, record.ID)
	}
	records[record.ID] = record
	return q.persistLocked(records, order)
}

func (q *PendingInputQueue) persistCurrentLocked() error {
	records, order := q.cloneStateLocked()
	return q.persistLocked(records, order)
}

func (q *PendingInputQueue) persistLocked(records map[string]PendingInputRecord, order []string) error {
	document := pendingInputDocument{Version: pendingInputDocumentVersion, Records: make([]PendingInputRecord, 0, len(order))}
	for _, id := range order {
		record, ok := records[id]
		if !ok {
			continue
		}
		if err := validatePendingInputRecord(record); err != nil {
			return fmt.Errorf("pending input queue: encode %q: %w", id, err)
		}
		document.Records = append(document.Records, record)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := q.writeFile(q.pathLocked(), data, 0o600, 0o700); err != nil {
		q.loaded = false
		return fmt.Errorf("pending input queue: write state: %w", err)
	}
	q.records = records
	q.order = append([]string(nil), order...)
	q.rebuildIndexesLocked()
	q.loaded = true
	if err := q.synchronizePendingCountLocked(false); err != nil {
		q.loaded = false
		return err
	}
	return nil
}

func (q *PendingInputQueue) synchronizePendingCountLocked(force bool) error {
	if q.thread == nil {
		return nil
	}
	count := q.materializedPendingCountLocked()
	if !force && q.thread.Info().PendingInputs == count {
		return nil
	}
	if err := q.thread.SetPendingInputCount(count); err != nil {
		return fmt.Errorf("pending input queue: synchronize Thread count: %w", err)
	}
	return nil
}

func (q *PendingInputQueue) applyTerminalLocked(disposition pendingTerminalDisposition, ids []string, message string) error {
	for _, id := range ids {
		q.handled[id] = struct{}{}
		if record, ok := q.records[id]; ok {
			q.handledMsgs[record.MessageID] = struct{}{}
		}
	}
	if disposition == pendingTerminalCompleted {
		for _, id := range ids {
			q.completed[id] = struct{}{}
		}
	}
	if !q.applyTerminalStateLocked(disposition, ids, message) {
		return nil
	}
	return q.persistCurrentLocked()
}

func (q *PendingInputQueue) applyTerminalStateLocked(disposition pendingTerminalDisposition, ids []string, message string) bool {
	if disposition == pendingTerminalNone || len(ids) == 0 {
		return false
	}
	changed := false
	for _, id := range ids {
		record, exists := q.records[id]
		if !exists {
			continue
		}
		switch disposition {
		case pendingTerminalCompleted, pendingTerminalCancelled:
			delete(q.records, id)
			q.order = removePendingInputID(q.order, id)
			changed = true
		case pendingTerminalRetryable:
			if record.State != PendingInputStateRetryable || record.LastError != message {
				record.State = PendingInputStateRetryable
				record.LastError = message
				q.records[id] = record
				changed = true
			}
		case pendingTerminalDeadLettered:
			if record.State != PendingInputStateDeadLettered || record.LastError != message {
				record.State = PendingInputStateDeadLettered
				record.LastError = message
				q.records[id] = record
				changed = true
			}
		}
	}
	if changed {
		q.rebuildIndexesLocked()
	}
	return changed
}

func pendingDispositionFromEvent(event events.Event) (pendingTerminalDisposition, []string, string) {
	if event.Type != "turn.completed" && event.Type != "turn.errored" && event.Type != "turn.cancelled" {
		return pendingTerminalNone, nil, ""
	}
	data, err := json.Marshal(event.Payload)
	if err != nil {
		return pendingTerminalNone, nil, ""
	}
	var payload struct {
		InputIDs  []string `json:"input_ids"`
		Error     string   `json:"error"`
		ErrorKind string   `json:"error_kind"`
	}
	if json.Unmarshal(data, &payload) != nil || len(payload.InputIDs) == 0 {
		return pendingTerminalNone, nil, ""
	}
	switch event.Type {
	case "turn.completed":
		return pendingTerminalCompleted, payload.InputIDs, ""
	case "turn.cancelled":
		return pendingTerminalCancelled, payload.InputIDs, payload.Error
	case "turn.errored":
		switch payload.ErrorKind {
		case "cancelled":
			return pendingTerminalCancelled, payload.InputIDs, payload.Error
		case "runtime_restart", "interrupted", "terminated":
			return pendingTerminalRetryable, payload.InputIDs, payload.Error
		default:
			return pendingTerminalDeadLettered, payload.InputIDs, payload.Error
		}
	}
	return pendingTerminalNone, nil, ""
}

func (q *PendingInputQueue) rebuildIndexesLocked() {
	q.messageIndex = map[string]string{}
	q.turnIndex = map[string]string{}
	for _, id := range q.order {
		record, ok := q.records[id]
		if !ok {
			continue
		}
		q.messageIndex[record.MessageID] = id
		if record.Origin == PendingInputOriginTurn &&
			(record.State == PendingInputStateAdmitted || record.State == PendingInputStateProcessed) && record.TurnID != "" {
			q.turnIndex[record.TurnID] = id
		}
	}
}

func (q *PendingInputQueue) materializedPendingCountLocked() int {
	count := 0
	for _, record := range q.records {
		switch record.State {
		case PendingInputStateAccepting, PendingInputStatePending, PendingInputStateRetryable:
			count++
		}
	}
	return count
}

func (q *PendingInputQueue) cloneStateLocked() (map[string]PendingInputRecord, []string) {
	return clonePendingInputRecords(q.records), append([]string(nil), q.order...)
}

func (q *PendingInputQueue) pathLocked() string {
	if q.thread != nil {
		return filepath.Join(q.thread.Dir, pendingInputFile)
	}
	return filepath.Join(q.dir, pendingInputFile)
}

func (q *PendingInputQueue) nowMillis() time.Time {
	return q.now().UTC().Truncate(time.Millisecond)
}

func clonePendingInputRecords(records map[string]PendingInputRecord) map[string]PendingInputRecord {
	out := make(map[string]PendingInputRecord, len(records))
	for id, record := range records {
		out[id] = record
	}
	return out
}

func removePendingInputID(order []string, id string) []string {
	for index := range order {
		if order[index] == id {
			copy(order[index:], order[index+1:])
			return order[:len(order)-1]
		}
	}
	return order
}

func nextUniquePendingInputID(records map[string]PendingInputRecord, next func() string) string {
	for {
		id := next()
		if _, exists := records[id]; !exists {
			return id
		}
	}
}

func newPendingInputID() string { return "pending-" + newID() }

func pendingInputMessageID(id string, createdAt time.Time) string {
	return thread.StableMessageID(createdAt, id)
}
