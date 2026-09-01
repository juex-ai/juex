package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

const pendingInputSummaryLength = 200

type PendingInputState string

const (
	PendingInputStateAccepting PendingInputState = "accepting"
	PendingInputStatePending   PendingInputState = "pending"
	PendingInputStateAdmitted  PendingInputState = "admitted"
	PendingInputStateProcessed PendingInputState = "processed"
	PendingInputStateExpired   PendingInputState = "expired"
	PendingInputStateDropped   PendingInputState = "dropped"
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
	Now    func() time.Time
	Thread *thread.Thread
}

// PendingInputRecoveryFacts are durable facts that can close a journal update
// interrupted by process termination. Admission-event message IDs prove that
// the exact accepting Turn input crossed the Framework boundary; transcript
// message IDs prove that accepted input was already consumed.
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
	ExpiresAt   time.Time          `json:"expires_at"`
	Attempts    int                `json:"attempts"`
	ProcessedAt *time.Time         `json:"processed_at,omitempty"`
}

func (r PendingInputRecord) Expired(now time.Time) bool {
	return r.Origin != PendingInputOriginTurn && isReplayablePendingState(r.State) && !r.ExpiresAt.IsZero() && !r.ExpiresAt.After(now)
}

type PendingInputQueue struct {
	thread            *thread.Thread
	now               func() time.Time
	mu                sync.Mutex
	loaded            bool
	records           map[string]PendingInputRecord
	replayable        map[string]struct{}
	messageIndex      map[string]string
	admittedTurnIndex map[string]string
	acceptanceOrder   map[string]uint64
	nextAcceptance    uint64
}

func NewPendingInputQueue(_ string, opts PendingInputQueueOptions) *PendingInputQueue {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PendingInputQueue{
		thread: opts.Thread,
		now:    now,
	}
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
		ID:        id,
		TurnID:    turnID,
		MessageID: msg.ID,
		Message:   msg,
		Summary:   truncate(msg.FirstText(), pendingInputSummaryLength),
		Origin:    PendingInputOriginQueued,
		State:     PendingInputStatePending,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := q.appendLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	q.indexRecordLocked(record)
	return record, nil
}

// AdmitTurnInput records the main input in one append before any Turn policy
// runs. Main inputs do not inherit the expiry of queued steering messages.
func (q *PendingInputQueue) AdmitTurnInput(turnID string, msg llm.Message, reuseTurnRecord bool) (PendingInputRecord, error) {
	return q.storeTurnInput(turnID, msg, reuseTurnRecord, PendingInputStateAdmitted)
}

// StageTurnInput writes a non-replayable intent for a new input. An existing
// durable pending record stays replayable until the caller commits admission.
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
		if id := q.admittedTurnIndex[turnID]; id != "" {
			return q.records[id], nil
		}
	}
	if msg.ID != "" {
		if id := q.messageIndex[msg.ID]; id != "" {
			record := q.records[id]
			if isReplayablePendingState(record.State) {
				// A persisted pending input was already accepted by the Framework.
				// Keep it replayable until the new Turn admission commits.
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
	record := PendingInputRecord{
		ID:        id,
		TurnID:    turnID,
		MessageID: msg.ID,
		Message:   msg,
		Summary:   truncate(msg.FirstText(), pendingInputSummaryLength),
		Origin:    PendingInputOriginTurn,
		State:     state,
		CreatedAt: now,
		Attempts:  1,
	}
	if err := q.appendLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	q.indexRecordLocked(record)
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
	if err := q.appendLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	q.indexRecordLocked(record)
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
	if record.State != PendingInputStateAccepting && !isReplayablePendingState(record.State) {
		return fmt.Errorf("pending input queue: admission intent %q has state %q for turn %q", id, record.State, record.TurnID)
	}
	wasAccepting := record.State == PendingInputStateAccepting
	record.Origin = PendingInputOriginTurn
	record.State = PendingInputStateAdmitted
	record.TurnID = turnID
	record.ExpiresAt = time.Time{}
	if !wasAccepting {
		record.Attempts++
	}
	if err := q.appendLocked(record); err != nil {
		return err
	}
	q.indexRecordLocked(record)
	return nil
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
	ordered := q.orderedReplayableLocked()
	out := make([]PendingInputRecord, 0, len(ordered))
	var expiredRecords []PendingInputRecord
	for _, record := range ordered {
		if record.State != PendingInputStatePending && record.State != PendingInputStateAdmitted {
			continue
		}
		if record.Expired(now) {
			record.State = PendingInputStateExpired
			record.TurnID = turnID
			expiredRecords = append(expiredRecords, record)
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, record)
	}
	if err := q.appendManyLocked(expiredRecords); err != nil {
		return nil, err
	}
	for _, record := range expiredRecords {
		q.indexRecordLocked(record)
	}
	return out, nil
}

func (q *PendingInputQueue) MarkAdmitted(ids []string, turnID string) error {
	return q.updateStates(ids, func(record PendingInputRecord, now time.Time) (PendingInputRecord, bool) {
		if record.State == PendingInputStatePending || record.State == PendingInputStateAdmitted {
			record.State = PendingInputStateAdmitted
			record.TurnID = turnID
			record.Attempts++
			return record, true
		}
		return record, false
	})
}

func (q *PendingInputQueue) PromoteToTurnInput(ids []string, turnID string) error {
	return q.updateStates(ids, func(record PendingInputRecord, now time.Time) (PendingInputRecord, bool) {
		if !isReplayablePendingState(record.State) {
			return record, false
		}
		if record.Origin == PendingInputOriginTurn && record.State == PendingInputStateAdmitted && record.TurnID == turnID {
			return record, false
		}
		record.Origin = PendingInputOriginTurn
		record.State = PendingInputStateAdmitted
		record.TurnID = turnID
		record.ExpiresAt = time.Time{}
		record.Attempts++
		return record, true
	})
}

func (q *PendingInputQueue) MarkProcessed(ids []string) error {
	return q.updateStates(ids, func(record PendingInputRecord, now time.Time) (PendingInputRecord, bool) {
		if record.State == PendingInputStatePending || record.State == PendingInputStateAdmitted {
			record.State = PendingInputStateProcessed
			record.ProcessedAt = &now
			return record, true
		}
		return record, false
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
	if !isReplayablePendingState(record.State) {
		return nil
	}
	now := q.nowMillis()
	record.State = PendingInputStateProcessed
	record.ProcessedAt = &now
	if err := q.appendLocked(record); err != nil {
		return err
	}
	q.indexRecordLocked(record)
	return nil
}

func (q *PendingInputQueue) MarkDropped(ids []string) error {
	return q.updateStates(ids, func(record PendingInputRecord, now time.Time) (PendingInputRecord, bool) {
		if record.State == PendingInputStateAccepting || record.State == PendingInputStatePending || record.State == PendingInputStateAdmitted {
			record.State = PendingInputStateDropped
			return record, true
		}
		return record, false
	})
}

func (q *PendingInputQueue) MarkExpired(ids []string) error {
	return q.updateStates(ids, func(record PendingInputRecord, _ time.Time) (PendingInputRecord, bool) {
		if isReplayablePendingState(record.State) {
			record.State = PendingInputStateExpired
			return record, true
		}
		return record, false
	})
}

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

// ReconcileRecoveryFacts advances only states proven by more authoritative
// durable facts. An accepting intent without a committed admission event stays
// inert, while explicit expired or dropped states are never resurrected.
func (q *PendingInputQueue) ReconcileRecoveryFacts(facts PendingInputRecoveryFacts) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}

	now := q.nowMillis()
	ordered := make([]PendingInputRecord, 0, len(q.records))
	for _, record := range q.records {
		ordered = append(ordered, record)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := q.acceptanceOrder[ordered[i].ID]
		right := q.acceptanceOrder[ordered[j].ID]
		if left != right {
			return left < right
		}
		return ordered[i].ID < ordered[j].ID
	})

	updates := make([]PendingInputRecord, 0)
	for _, record := range ordered {
		if _, ok := facts.TranscriptMessageIDs[record.MessageID]; ok &&
			(record.State == PendingInputStateAccepting || isReplayablePendingState(record.State)) {
			record.State = PendingInputStateProcessed
			record.ProcessedAt = &now
			updates = append(updates, record)
			continue
		}
		if record.State != PendingInputStateAccepting || record.MessageID == "" {
			continue
		}
		if _, ok := facts.AdmittedMessageIDs[record.MessageID]; !ok {
			continue
		}
		record.Origin = PendingInputOriginTurn
		record.State = PendingInputStateAdmitted
		record.ExpiresAt = time.Time{}
		updates = append(updates, record)
	}
	if err := q.appendManyLocked(updates); err != nil {
		return err
	}
	for _, record := range updates {
		q.indexRecordLocked(record)
	}
	return nil
}

func (q *PendingInputQueue) updateStates(ids []string, update func(PendingInputRecord, time.Time) (PendingInputRecord, bool)) error {
	if q == nil || len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	now := q.nowMillis()
	var updatedRecords []PendingInputRecord
	for _, id := range ids {
		record, ok := q.records[id]
		if !ok {
			continue
		}
		updated, changed := update(record, now)
		if !changed {
			continue
		}
		updatedRecords = append(updatedRecords, updated)
	}
	if err := q.appendManyLocked(updatedRecords); err != nil {
		return err
	}
	for _, record := range updatedRecords {
		q.indexRecordLocked(record)
	}
	return nil
}

func (q *PendingInputQueue) ensureLoadedLocked() error {
	if q.loaded {
		return nil
	}
	if q.thread == nil {
		return fmt.Errorf("pending input queue: Thread journal is required")
	}
	q.acceptanceOrder = map[string]uint64{}
	q.nextAcceptance = 0
	records, err := q.loadLatestLocked()
	if err != nil {
		return err
	}
	q.records = records
	q.replayable = map[string]struct{}{}
	q.messageIndex = map[string]string{}
	q.admittedTurnIndex = map[string]string{}
	for _, record := range records {
		q.indexRecordLocked(record)
	}
	q.loaded = true
	return nil
}

func (q *PendingInputQueue) indexRecordLocked(record PendingInputRecord) {
	if q.records == nil {
		q.records = map[string]PendingInputRecord{}
	}
	if q.replayable == nil {
		q.replayable = map[string]struct{}{}
	}
	if q.messageIndex == nil {
		q.messageIndex = map[string]string{}
	}
	if q.admittedTurnIndex == nil {
		q.admittedTurnIndex = map[string]string{}
	}
	q.trackAcceptanceLocked(record.ID)
	if previous, ok := q.records[record.ID]; ok {
		delete(q.replayable, previous.ID)
		if q.messageIndex[previous.MessageID] == previous.ID {
			delete(q.messageIndex, previous.MessageID)
		}
		if previous.Origin == PendingInputOriginTurn && q.admittedTurnIndex[previous.TurnID] == previous.ID {
			delete(q.admittedTurnIndex, previous.TurnID)
		}
	}
	q.records[record.ID] = record
	q.messageIndex[record.MessageID] = record.ID
	if isReplayablePendingState(record.State) {
		q.replayable[record.ID] = struct{}{}
		if record.Origin == PendingInputOriginTurn && record.State == PendingInputStateAdmitted {
			q.admittedTurnIndex[record.TurnID] = record.ID
		}
	}
}

func (q *PendingInputQueue) orderedReplayableLocked() []PendingInputRecord {
	out := make([]PendingInputRecord, 0, len(q.replayable))
	for id := range q.replayable {
		out = append(out, q.records[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := q.acceptanceOrder[out[i].ID]
		right := q.acceptanceOrder[out[j].ID]
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (q *PendingInputQueue) trackAcceptanceLocked(id string) {
	if id == "" {
		return
	}
	if q.acceptanceOrder == nil {
		q.acceptanceOrder = map[string]uint64{}
	}
	if q.acceptanceOrder[id] != 0 {
		return
	}
	q.nextAcceptance++
	q.acceptanceOrder[id] = q.nextAcceptance
}

func clonePendingInputRecords(records map[string]PendingInputRecord) map[string]PendingInputRecord {
	out := make(map[string]PendingInputRecord, len(records))
	for id, record := range records {
		out[id] = record
	}
	return out
}

func nextUniquePendingInputID(records map[string]PendingInputRecord, next func() string) string {
	for {
		id := next()
		if _, exists := records[id]; !exists {
			return id
		}
	}
}

func newPendingInputID() string {
	return "pending-" + newID()
}

func (q *PendingInputQueue) loadLatestLocked() (map[string]PendingInputRecord, error) {
	records := map[string]PendingInputRecord{}
	replay := q.thread.ReplaySnapshot()
	for _, id := range replay.InputOrder {
		raw, ok := replay.InputRecords[id]
		if !ok {
			return nil, fmt.Errorf("pending input queue: Thread journal order references missing record %q", id)
		}
		var record PendingInputRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("pending input queue: decode Thread journal record %q: %w", id, err)
		}
		if record.ID != id {
			return nil, fmt.Errorf("pending input queue: Thread journal record id mismatch %q", id)
		}
		q.trackAcceptanceLocked(id)
		records[id] = record
	}
	if len(records) != len(replay.InputRecords) {
		return nil, fmt.Errorf("pending input queue: Thread journal input order is incomplete")
	}
	return records, nil
}

func (q *PendingInputQueue) appendLocked(record PendingInputRecord) error {
	return q.appendManyLocked([]PendingInputRecord{record})
}

func (q *PendingInputQueue) appendManyLocked(records []PendingInputRecord) error {
	if len(records) == 0 {
		return nil
	}
	facts := make([]thread.Fact, 0, 64)
	flush := func() error {
		if len(facts) == 0 {
			return nil
		}
		if _, err := q.thread.AppendFacts(facts...); err != nil {
			q.loaded = false
			return err
		}
		facts = facts[:0]
		return nil
	}
	for _, record := range records {
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		recordFacts := append(q.inputLifecycleFacts(record), thread.Fact{
			Type: thread.FactInputRecorded, InputID: record.ID, InputRecord: body,
		})
		if len(facts)+len(recordFacts) > 64 {
			if err := flush(); err != nil {
				return err
			}
		}
		facts = append(facts, recordFacts...)
	}
	return flush()
}

func (q *PendingInputQueue) nowMillis() time.Time {
	return q.now().UTC().Truncate(time.Millisecond)
}

func (q *PendingInputQueue) inputLifecycleFacts(record PendingInputRecord) []thread.Fact {
	if q == nil || q.thread == nil || record.ID == "" {
		return nil
	}
	previous, existed := q.records[record.ID]
	attemptID := fmt.Sprintf("ia_%s_%d", record.ID, max(record.Attempts, 1))
	generationID := q.thread.Projection().CurrentGeneration.ID
	var facts []thread.Fact
	if !existed && record.State == PendingInputStatePending {
		facts = append(facts, thread.Fact{Type: thread.FactInputAccepted, InputID: record.ID, Message: &record.Message})
	}
	starting := record.State == PendingInputStateAdmitted &&
		(!existed || previous.State == PendingInputStateAccepting || previous.State == PendingInputStatePending)
	if starting {
		if !existed || previous.State == PendingInputStateAccepting {
			facts = append(facts, thread.Fact{Type: thread.FactInputAccepted, InputID: record.ID, Message: &record.Message})
		}
		facts = append(facts, thread.Fact{
			Type: thread.FactInputAttemptStart, InputID: record.ID, AttemptID: attemptID,
			GenerationID: generationID, TurnID: record.TurnID,
		})
	}
	if !existed {
		return facts
	}
	if previous.State == PendingInputStateAdmitted {
		switch record.State {
		case PendingInputStateDropped:
			previousAttemptID := fmt.Sprintf("ia_%s_%d", record.ID, max(previous.Attempts, 1))
			facts = append(facts,
				thread.Fact{Type: thread.FactInputAttemptCancel, InputID: record.ID, AttemptID: previousAttemptID},
				thread.Fact{Type: thread.FactInputCancelled, InputID: record.ID},
			)
		}
	} else if previous.State == PendingInputStatePending {
		switch record.State {
		case PendingInputStateDropped:
			facts = append(facts, thread.Fact{Type: thread.FactInputCancelled, InputID: record.ID})
		case PendingInputStateExpired:
			facts = append(facts, thread.Fact{Type: thread.FactInputExpired, InputID: record.ID})
		}
	}
	return facts
}

func pendingInputMessageID(id string, createdAt time.Time) string {
	return thread.StableMessageID(createdAt, id)
}
