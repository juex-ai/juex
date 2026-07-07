package observable

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	RunStateStarting = "starting"
	RunStateRunning  = "running"
	RunStateStopped  = "stopped"
	RunStateExited   = "exited"
	RunStateErrored  = "errored"

	ObservationStateRecorded  = "recorded"
	ObservationStateQueued    = "queued"
	ObservationStateDelivered = "delivered"
	ObservationStateDropped   = "dropped"
)

var ErrObservationNotFound = errors.New("observable: observation not found")

type RunRecord struct {
	ObservableID string    `json:"observable_id"`
	RunID        string    `json:"run_id"`
	State        string    `json:"state"`
	PID          int       `json:"pid,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	ExitedAt     time.Time `json:"exited_at,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type ObservationRecord struct {
	ID             string    `json:"id"`
	ObservableID   string    `json:"observable_id"`
	RunID          string    `json:"run_id,omitempty"`
	SourceEventID  string    `json:"source_event_id,omitempty"`
	Kind           string    `json:"kind"`
	Severity       string    `json:"severity"`
	Stream         string    `json:"stream,omitempty"`
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end"`
	Content        string    `json:"content"`
	OriginalChars  int       `json:"original_chars"`
	Truncated      bool      `json:"truncated,omitempty"`
	ArtifactPath   string    `json:"artifact_path,omitempty"`
	State          string    `json:"state"`
	TargetSession  string    `json:"target_session,omitempty"`
	PendingInputID string    `json:"pending_input_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	DeliveredAt    time.Time `json:"delivered_at,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type ScheduleStateRecord struct {
	ObservableID           string    `json:"observable_id"`
	LastEvaluatedAt        time.Time `json:"last_evaluated_at,omitempty"`
	LastEmittedScheduledAt time.Time `json:"last_emitted_scheduled_at,omitempty"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type StoreOptions struct {
	Now func() time.Time
}

type Store struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

type ObservationFilter struct {
	ObservableID string
	Limit        int
}

func NewStore(root string, opts StoreOptions) *Store {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Store{root: root, now: now}
}

func (s *Store) AppendRun(record RunRecord) error {
	if s == nil {
		return fmt.Errorf("observable store: nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendJSONL(filepath.Join(s.root, "runs.jsonl"), record)
}

func (s *Store) LatestRuns() (map[string]RunRecord, error) {
	if s == nil {
		return map[string]RunRecord{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]RunRecord{}
	err := readJSONL(filepath.Join(s.root, "runs.jsonl"), func(record RunRecord) {
		if record.ObservableID != "" {
			out[record.ObservableID] = record
		}
	})
	return out, err
}

func (s *Store) RecordObservation(record ObservationRecord) (ObservationRecord, error) {
	if s == nil {
		return ObservationRecord{}, fmt.Errorf("observable store: nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.ID == "" {
		record.ID = BuildObservationID(record)
	}
	if record.State == "" {
		record.State = ObservationStateRecorded
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = s.now().UTC()
	}
	if record.OriginalChars == 0 {
		record.OriginalChars = len([]rune(record.Content))
	}
	records, err := loadObservations(filepath.Join(s.root, "observations.jsonl"))
	if err != nil {
		return ObservationRecord{}, err
	}
	if existing, ok := records[record.ID]; ok {
		return existing, nil
	}
	if err := appendJSONL(filepath.Join(s.root, "observations.jsonl"), record); err != nil {
		return ObservationRecord{}, err
	}
	return record, nil
}

func (s *Store) UpdateObservation(id string, update func(ObservationRecord) ObservationRecord) error {
	if s == nil {
		return fmt.Errorf("observable store: nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "observations.jsonl")
	records, err := loadObservations(path)
	if err != nil {
		return err
	}
	record, ok := records[id]
	if !ok {
		return ErrObservationNotFound
	}
	updated := update(record)
	if updated.ID == "" {
		updated.ID = id
	}
	return appendJSONL(path, updated)
}

func (s *Store) ListObservations(filter ObservationFilter) ([]ObservationRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := loadObservations(filepath.Join(s.root, "observations.jsonl"))
	if err != nil {
		return nil, err
	}
	out := make([]ObservationRecord, 0, len(records))
	for _, record := range records {
		if filter.ObservableID != "" && record.ObservableID != filter.ObservableID {
			continue
		}
		out = append(out, record)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *Store) FindObservationBySourceEventID(sourceEventID string) (ObservationRecord, bool, error) {
	if s == nil || sourceEventID == "" {
		return ObservationRecord{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := loadObservations(filepath.Join(s.root, "observations.jsonl"))
	if err != nil {
		return ObservationRecord{}, false, err
	}
	for _, record := range records {
		if record.SourceEventID == sourceEventID {
			return record, true, nil
		}
	}
	return ObservationRecord{}, false, nil
}

func (s *Store) LatestScheduleStates() (map[string]ScheduleStateRecord, error) {
	if s == nil {
		return map[string]ScheduleStateRecord{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]ScheduleStateRecord{}
	err := readJSONL(filepath.Join(s.root, "schedule_state.jsonl"), func(record ScheduleStateRecord) {
		if record.ObservableID != "" {
			out[record.ObservableID] = record
		}
	})
	return out, err
}

func (s *Store) ScheduleState(id string) (ScheduleStateRecord, bool, error) {
	states, err := s.LatestScheduleStates()
	if err != nil {
		return ScheduleStateRecord{}, false, err
	}
	record, ok := states[id]
	return record, ok, nil
}

func (s *Store) RecordScheduleState(record ScheduleStateRecord) error {
	if s == nil {
		return fmt.Errorf("observable store: nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = s.now().UTC()
	}
	return appendJSONL(filepath.Join(s.root, "schedule_state.jsonl"), record)
}

func (s *Store) ArtifactPath(observableID, observationID string) string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.root, "artifacts", observableID, observationID+".txt")
}

func BuildObservationID(record ObservationRecord) string {
	if record.SourceEventID != "" {
		sum := sha256.Sum256([]byte(record.ObservableID + "\x00" + record.SourceEventID))
		return "obs-" + hex.EncodeToString(sum[:8])
	}
	contentHash := sha256.Sum256([]byte(record.Content))
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		record.ObservableID,
		record.RunID,
		record.WindowStart.UTC().Format(time.RFC3339Nano),
		record.WindowEnd.UTC().Format(time.RFC3339Nano),
		record.Kind,
		record.Severity,
		hex.EncodeToString(contentHash[:]),
	)
	sum := sha256.Sum256([]byte(key))
	return "obs-" + hex.EncodeToString(sum[:8])
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = file.Write(body)
	return err
}

func loadObservations(path string) (map[string]ObservationRecord, error) {
	records := map[string]ObservationRecord{}
	err := readJSONL(path, func(record ObservationRecord) {
		if record.ID != "" {
			records[record.ID] = record
		}
	})
	return records, err
}

func readJSONL[T any](path string, accept func(T)) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for line := 1; ; line++ {
		text, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if trimmed := stringsTrimSpace(text); trimmed != "" {
			var value T
			if decodeErr := json.Unmarshal([]byte(trimmed), &value); decodeErr != nil {
				return fmt.Errorf("observable store: parse %s:%d: %w", path, line, decodeErr)
			}
			accept(value)
		}
		if err == io.EOF {
			break
		}
	}
	return nil
}

func stringsTrimSpace(s string) string {
	for len(s) > 0 {
		switch s[0] {
		case ' ', '\n', '\r', '\t':
			s = s[1:]
		default:
			goto trimRight
		}
	}
trimRight:
	for len(s) > 0 {
		switch s[len(s)-1] {
		case ' ', '\n', '\r', '\t':
			s = s[:len(s)-1]
		default:
			return s
		}
	}
	return s
}
