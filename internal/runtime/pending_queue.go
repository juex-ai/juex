package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

const (
	pendingInputFile          = "pending_input.jsonl"
	pendingInputSummaryLength = 200
)

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
	Now func() time.Time
}

type pendingInputFileOps struct {
	write func(*os.File, []byte) (int, error)
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
	path              string
	now               func() time.Time
	mu                sync.Mutex
	loaded            bool
	records           map[string]PendingInputRecord
	replayable        map[string]struct{}
	messageIndex      map[string]string
	admittedTurnIndex map[string]string
	acceptanceOrder   map[string]uint64
	nextAcceptance    uint64
	journalExists     bool
	journalSize       int64
	journalInfo       os.FileInfo
	fileOps           pendingInputFileOps
}

func NewPendingInputQueue(sessionDir string, opts PendingInputQueueOptions) *PendingInputQueue {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PendingInputQueue{
		path:    filepath.Join(sessionDir, pendingInputFile),
		now:     now,
		fileOps: pendingInputFileOps{write: func(file *os.File, body []byte) (int, error) { return file.Write(body) }},
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
	now := q.now().UTC()
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
		}
	}

	id := nextUniquePendingInputID(q.records, newPendingInputID)
	now := q.now().UTC()
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
	now := q.now().UTC()
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
	now := q.now().UTC()
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

func (q *PendingInputQueue) updateStates(ids []string, update func(PendingInputRecord, time.Time) (PendingInputRecord, bool)) error {
	if q == nil || len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if err := q.ensureLoadedLocked(); err != nil {
		return err
	}
	now := q.now().UTC()
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
		return q.validateJournalLocked()
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

func (q *PendingInputQueue) validateJournalLocked() error {
	info, err := os.Stat(q.path)
	if err != nil {
		if os.IsNotExist(err) && !q.journalExists {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("pending input queue: journal path is a directory: %s", q.path)
	}
	if q.journalExists && q.journalInfo != nil && !os.SameFile(q.journalInfo, info) {
		return fmt.Errorf("pending input queue: journal replaced outside active runtime: %s", q.path)
	}
	if info.Size() < q.journalSize {
		return fmt.Errorf("pending input queue: journal changed outside active runtime: %s", q.path)
	}
	if info.Size() == q.journalSize && q.journalInfo != nil && !info.ModTime().Equal(q.journalInfo.ModTime()) {
		return fmt.Errorf("pending input queue: journal modified outside active runtime: %s", q.path)
	}
	if !q.journalExists || info.Size() > q.journalSize {
		return q.loadAppendedLocked()
	}
	return nil
}

func (q *PendingInputQueue) loadAppendedLocked() error {
	file, err := os.Open(q.path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(q.journalSize, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	for {
		text, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		text = strings.TrimSpace(text)
		if text != "" {
			var record PendingInputRecord
			if err := json.Unmarshal([]byte(text), &record); err != nil {
				return fmt.Errorf("pending input queue: parse appended %s: %w", q.path, err)
			}
			if record.ID != "" {
				q.indexRecordLocked(record)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	q.journalExists = true
	q.journalSize = info.Size()
	q.journalInfo = info
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
	file, err := os.Open(q.path)
	if err != nil {
		if os.IsNotExist(err) {
			q.journalExists = false
			q.journalSize = 0
			q.journalInfo = nil
			return records, nil
		}
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for line := 1; ; line++ {
		text, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			if err == io.EOF {
				break
			}
			continue
		}
		var record PendingInputRecord
		if decodeErr := json.Unmarshal([]byte(text), &record); decodeErr != nil {
			return nil, fmt.Errorf("pending input queue: parse %s:%d: %w", q.path, line, decodeErr)
		}
		if record.ID == "" {
			if err == io.EOF {
				break
			}
			continue
		}
		q.trackAcceptanceLocked(record.ID)
		records[record.ID] = record
		if err == io.EOF {
			break
		}
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	q.journalExists = true
	q.journalSize = info.Size()
	q.journalInfo = info
	return records, nil
}

func (q *PendingInputQueue) appendLocked(record PendingInputRecord) error {
	return q.appendManyLocked([]PendingInputRecord{record})
}

func (q *PendingInputQueue) appendManyLocked(records []PendingInputRecord) error {
	if len(records) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(q.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	var written int64
	for _, record := range records {
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		body = append(body, '\n')
		n, err := q.fileOps.write(file, body)
		written += int64(n)
		if err != nil {
			return err
		}
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != q.journalSize+written {
		return fmt.Errorf("pending input queue: concurrent journal append: %s", q.path)
	}
	q.journalExists = true
	q.journalSize = info.Size()
	q.journalInfo = info
	return nil
}

func pendingInputMessageID(id string, createdAt time.Time) string {
	return session.StableMessageID(createdAt, id)
}
