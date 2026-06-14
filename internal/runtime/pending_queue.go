package runtime

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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
)

const (
	pendingInputFile          = "pending_input.jsonl"
	pendingInputSummaryLength = 200
)

type PendingInputState string

const (
	PendingInputStatePending   PendingInputState = "pending"
	PendingInputStateAdmitted  PendingInputState = "admitted"
	PendingInputStateProcessed PendingInputState = "processed"
	PendingInputStateExpired   PendingInputState = "expired"
	PendingInputStateDropped   PendingInputState = "dropped"
)

type PendingInputOptions struct {
	ID  string
	TTL time.Duration
}

type PendingInputQueueOptions struct {
	Now func() time.Time
}

type PendingInputRecord struct {
	ID          string            `json:"id"`
	TurnID      string            `json:"turn_id,omitempty"`
	MessageID   string            `json:"message_id"`
	Message     llm.Message       `json:"message"`
	Summary     string            `json:"summary,omitempty"`
	State       PendingInputState `json:"state"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Attempts    int               `json:"attempts"`
	ProcessedAt *time.Time        `json:"processed_at,omitempty"`
}

type PendingInputQueue struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func NewPendingInputQueue(sessionDir string, opts PendingInputQueueOptions) *PendingInputQueue {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PendingInputQueue{
		path: filepath.Join(sessionDir, pendingInputFile),
		now:  now,
	}
}

func (q *PendingInputQueue) Enqueue(msg llm.Message, opts PendingInputOptions, turnID string) (PendingInputRecord, error) {
	if q == nil {
		return PendingInputRecord{}, fmt.Errorf("pending input queue: nil store")
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	records, err := q.loadLatestLocked()
	if err != nil {
		return PendingInputRecord{}, err
	}
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = "pending-" + newID()
	}
	if existing, ok := records[id]; ok {
		return existing, nil
	}
	now := q.now().UTC()
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultPendingInputTTL
	}
	msg.ID = pendingInputMessageID(id)
	if msg.Blocks == nil {
		msg.Blocks = []llm.Block{}
	}
	record := PendingInputRecord{
		ID:        id,
		TurnID:    turnID,
		MessageID: msg.ID,
		Message:   msg,
		Summary:   truncate(msg.FirstText(), pendingInputSummaryLength),
		State:     PendingInputStatePending,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := q.appendLocked(record); err != nil {
		return PendingInputRecord{}, err
	}
	return record, nil
}

func (q *PendingInputQueue) Replayable(turnID string, limit int) ([]PendingInputRecord, error) {
	if q == nil {
		return nil, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	records, err := q.loadLatestLocked()
	if err != nil {
		return nil, err
	}
	now := q.now().UTC()
	ordered := orderedPendingInputRecords(records)
	out := make([]PendingInputRecord, 0, len(ordered))
	for _, record := range ordered {
		if record.State != PendingInputStatePending && record.State != PendingInputStateAdmitted {
			continue
		}
		if !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(now) {
			record.State = PendingInputStateExpired
			record.TurnID = turnID
			if err := q.appendLocked(record); err != nil {
				return nil, err
			}
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

func (q *PendingInputQueue) MarkDropped(ids []string) error {
	return q.updateStates(ids, func(record PendingInputRecord, now time.Time) (PendingInputRecord, bool) {
		if record.State == PendingInputStatePending || record.State == PendingInputStateAdmitted {
			record.State = PendingInputStateDropped
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
	return q.loadLatestLocked()
}

func (q *PendingInputQueue) updateStates(ids []string, update func(PendingInputRecord, time.Time) (PendingInputRecord, bool)) error {
	if q == nil || len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	records, err := q.loadLatestLocked()
	if err != nil {
		return err
	}
	now := q.now().UTC()
	for _, id := range ids {
		record, ok := records[id]
		if !ok {
			continue
		}
		updated, changed := update(record, now)
		if !changed {
			continue
		}
		if err := q.appendLocked(updated); err != nil {
			return err
		}
		records[id] = updated
	}
	return nil
}

func (q *PendingInputQueue) loadLatestLocked() (map[string]PendingInputRecord, error) {
	records := map[string]PendingInputRecord{}
	file, err := os.Open(q.path)
	if err != nil {
		if os.IsNotExist(err) {
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
		records[record.ID] = record
		if err == io.EOF {
			break
		}
	}
	return records, nil
}

func (q *PendingInputQueue) appendLocked(record PendingInputRecord) error {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(q.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = file.Write(body)
	return err
}

func orderedPendingInputRecords(records map[string]PendingInputRecord) []PendingInputRecord {
	out := make([]PendingInputRecord, 0, len(records))
	for _, record := range records {
		out = append(out, record)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func pendingInputMessageID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "msg-pending-" + hex.EncodeToString(sum[:8])
}
