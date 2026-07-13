package workmem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const goalStateFile = "goal_state.json"

type GoalStatus string

const (
	GoalStatusInProgress GoalStatus = "in_progress"
	GoalStatusSuccess    GoalStatus = "success"
	GoalStatusFailure    GoalStatus = "failure"
)

type GoalState struct {
	Version           int        `json:"version"`
	Description       string     `json:"description,omitempty"`
	Acceptance        string     `json:"acceptance,omitempty"`
	ContinuationCount int        `json:"continuation_count,omitempty"`
	Status            GoalStatus `json:"status,omitempty"`
	StatusReason      string     `json:"status_reason,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at,omitempty"`
}

type GoalStateUpdate struct {
	Description  *string    `json:"description,omitempty"`
	Acceptance   *string    `json:"acceptance,omitempty"`
	Status       GoalStatus `json:"status,omitempty"`
	StatusReason *string    `json:"status_reason,omitempty"`
}

type GoalStateCreate struct {
	Description  string `json:"description,omitempty"`
	Acceptance   string `json:"acceptance,omitempty"`
	StatusReason string `json:"status_reason,omitempty"`
}

type GoalStateOptions struct {
	Now func() time.Time
}

type GoalStateStore struct {
	SessionDir string
	Path       string
	Now        func() time.Time

	mu sync.Mutex
}

type GoalGateDecision struct {
	Status            GoalStatus `json:"status,omitempty"`
	BlockStop         bool       `json:"block_stop,omitempty"`
	ContinuePrompt    string     `json:"continue_prompt,omitempty"`
	Reason            string     `json:"reason,omitempty"`
	ContinuationCount int        `json:"continuation_count,omitempty"`
}

type GoalStatusSnapshot struct {
	Description       string     `json:"description,omitempty"`
	Acceptance        string     `json:"acceptance,omitempty"`
	ContinuationCount int        `json:"continuation_count,omitempty"`
	Status            GoalStatus `json:"status,omitempty"`
	StatusReason      string     `json:"status_reason,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at,omitempty"`
}

func NewGoalStateStore(sessionDir string, opts GoalStateOptions) *GoalStateStore {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &GoalStateStore{
		SessionDir: sessionDir,
		Path:       filepath.Join(sessionDir, goalStateFile),
		Now:        now,
	}
}

func (s *GoalStateStore) Snapshot() (GoalState, error) {
	if s == nil {
		return GoalState{Version: 1}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *GoalStateStore) Create(description, acceptance string) (GoalState, error) {
	return s.CreateWithContract(GoalStateCreate{
		Description: description,
		Acceptance:  acceptance,
	})
}

func (s *GoalStateStore) CreateWithContract(create GoalStateCreate) (GoalState, error) {
	if s == nil {
		return GoalState{Version: 1}, nil
	}
	description := sanitizeGoalText(create.Description)
	if description == "" {
		return GoalState{}, fmt.Errorf("goal description is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := GoalState{
		Version:           1,
		Description:       description,
		Acceptance:        sanitizeGoalText(create.Acceptance),
		ContinuationCount: 0,
		Status:            GoalStatusInProgress,
		StatusReason:      sanitizeGoalText(create.StatusReason),
		UpdatedAt:         s.now(),
	}
	if err := s.saveLocked(state); err != nil {
		return GoalState{}, err
	}
	return state, nil
}

func (s *GoalStateStore) Update(update GoalStateUpdate) (GoalState, error) {
	if s == nil {
		return GoalState{Version: 1}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return GoalState{}, err
	}
	if state.StatusSnapshot() == nil {
		return GoalState{}, fmt.Errorf("goal is not set")
	}
	if update.Description != nil {
		state.Description = sanitizeGoalText(*update.Description)
		if state.Description == "" {
			return GoalState{}, fmt.Errorf("goal description cannot be empty")
		}
	}
	if update.Acceptance != nil {
		state.Acceptance = sanitizeGoalText(*update.Acceptance)
	}
	if update.Status != "" {
		if err := validateGoalStatus(update.Status); err != nil {
			return GoalState{}, err
		}
		state.Status = update.Status
	}
	if update.StatusReason != nil {
		state.StatusReason = sanitizeGoalText(*update.StatusReason)
	}
	if state.Status == "" {
		state.Status = GoalStatusInProgress
	}
	state.UpdatedAt = s.now()
	if err := s.saveLocked(state); err != nil {
		return GoalState{}, err
	}
	return state, nil
}

func (s *GoalStateStore) CompletionGateDecision() (GoalGateDecision, error) {
	if s == nil {
		return GoalGateDecision{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return GoalGateDecision{}, err
	}
	if state.StatusSnapshot() == nil || state.Status == GoalStatusSuccess || state.Status == GoalStatusFailure {
		return GoalGateDecision{Status: state.Status, ContinuationCount: state.ContinuationCount}, nil
	}
	if state.Status != GoalStatusInProgress {
		return GoalGateDecision{Status: state.Status, ContinuationCount: state.ContinuationCount}, nil
	}
	prompt := "The current session goal is still in progress. Continue working toward the goal, or call update_goal with status success or failure when the goal is complete or cannot be completed."
	if contract, ok := state.RenderProviderContext(); ok {
		prompt = "The current session goal is still in progress.\n\n" + contract +
			"\n\nContinue working, or call update_goal with status success or failure when the goal is complete or cannot be completed."
	}
	return GoalGateDecision{
		Status:            state.Status,
		BlockStop:         true,
		ContinuePrompt:    prompt,
		Reason:            "goal_in_progress",
		ContinuationCount: state.ContinuationCount,
	}, nil
}

func (s *GoalStateStore) RecordContinuation(decision GoalGateDecision) error {
	if s == nil || !decision.BlockStop {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	if state.StatusSnapshot() == nil {
		return nil
	}
	state.ContinuationCount++
	state.UpdatedAt = s.now()
	return s.saveLocked(state)
}

func (s *GoalStateStore) StatusSnapshot() (*GoalStatusSnapshot, error) {
	if s == nil {
		return nil, nil
	}
	state, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	return state.StatusSnapshot(), nil
}

func (s GoalState) StatusSnapshot() *GoalStatusSnapshot {
	if !s.present() {
		return nil
	}
	return &GoalStatusSnapshot{
		Description:       s.Description,
		Acceptance:        s.Acceptance,
		ContinuationCount: s.ContinuationCount,
		Status:            s.Status,
		StatusReason:      s.StatusReason,
		UpdatedAt:         s.UpdatedAt,
	}
}

func (s GoalState) RawMessage() json.RawMessage {
	if s.StatusSnapshot() == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return data
}

func (s *GoalStateStore) loadLocked() (GoalState, error) {
	state := GoalState{Version: 1}
	if s == nil || s.Path == "" {
		return state, nil
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("goal state read: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("goal state parse: %w", err)
	}
	state = normalizeGoalState(state)
	return state, nil
}

func (s *GoalStateStore) saveLocked(state GoalState) error {
	if s == nil || s.Path == "" {
		return nil
	}
	state = normalizeGoalState(state)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("goal state mkdir: %w", err)
	}
	data, err := json.MarshalIndent(redactGoalState(state), "", "  ")
	if err != nil {
		return fmt.Errorf("goal state encode: %w", err)
	}
	data = append(data, '\n')
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("goal state write: %w", err)
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("goal state replace: %w", err)
	}
	return nil
}

func (s *GoalStateStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeGoalState(state GoalState) GoalState {
	state.Version = 1
	state.Description = sanitizeGoalText(state.Description)
	state.Acceptance = sanitizeGoalText(state.Acceptance)
	state.StatusReason = sanitizeGoalText(state.StatusReason)
	if state.ContinuationCount < 0 {
		state.ContinuationCount = 0
	}
	if state.Status != "" {
		if err := validateGoalStatus(state.Status); err != nil {
			// An explicit unknown status identifies an incompatible contract.
			return GoalState{Version: 1}
		}
	}
	if state.present() {
		if state.Status == "" {
			state.Status = GoalStatusInProgress
		}
	}
	return state
}

func validateGoalStatus(status GoalStatus) error {
	switch status {
	case GoalStatusInProgress, GoalStatusSuccess, GoalStatusFailure:
		return nil
	default:
		return fmt.Errorf("invalid goal status %q", status)
	}
}

func redactGoalState(state GoalState) GoalState {
	state.Description = sanitizeGoalText(state.Description)
	state.Acceptance = sanitizeGoalText(state.Acceptance)
	state.StatusReason = sanitizeGoalText(state.StatusReason)
	return state
}

func sanitizeGoalText(text string) string {
	return sanitizeWorkingStateText(text)
}

func (s GoalState) present() bool {
	return strings.TrimSpace(s.Description) != "" ||
		strings.TrimSpace(s.Acceptance) != "" ||
		s.Status != ""
}

func (s GoalState) RenderProviderContext() (string, bool) {
	if !s.present() {
		return "", false
	}
	var b strings.Builder
	b.WriteString("Current goal contract (model-owned; update with update_goal when the contract or evidence changes):\n")
	writeGoalContextValue(&b, "description", s.Description)
	writeGoalContextValue(&b, "acceptance", s.Acceptance)
	if s.Status != "" {
		writeGoalContextValue(&b, "status", string(s.Status))
	}
	writeGoalContextValue(&b, "status reason", s.StatusReason)
	return strings.TrimRight(b.String(), "\n"), true
}

func writeGoalContextValue(b *strings.Builder, label, value string) {
	value = compactGoalContextLine(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", label, value)
}

func compactGoalContextLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
