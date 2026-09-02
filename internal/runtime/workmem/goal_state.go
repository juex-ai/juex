package workmem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const goalStateFile = "goal_state.json"

type GoalStatus string

const (
	GoalStatusInProgress  GoalStatus = "in_progress"
	GoalStatusWaitForUser GoalStatus = "wait_for_user"
	GoalStatusSuccess     GoalStatus = "success"
	GoalStatusFailure     GoalStatus = "failure"

	maxGoalAcceptanceBytes = 32 * 1024
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
	ThreadDir string
	Path      string
	Now       func() time.Time

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

func NewGoalStateStore(threadDir string, opts GoalStateOptions) *GoalStateStore {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &GoalStateStore{
		ThreadDir: threadDir,
		Path:      filepath.Join(threadDir, goalStateFile),
		Now:       now,
	}
}

func (s *GoalStateStore) Clear() error {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := clearFileWithRollback(s.Path); err != nil {
		return fmt.Errorf("goal state clear: %w", err)
	}
	return nil
}

func (s *GoalStateStore) ClearWithRollback() (func() error, error) {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return func() error { return nil }, nil
	}
	s.mu.Lock()
	rollback, err := clearFileWithRollback(s.Path)
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("goal state clear: %w", err)
	}
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := rollback(); err != nil {
			return fmt.Errorf("goal state restore: %w", err)
		}
		return nil
	}, nil
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
		Acceptance:        sanitizeGoalAcceptance(create.Acceptance),
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
		state.Acceptance = sanitizeGoalAcceptance(*update.Acceptance)
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
	prompt := "The current thread goal is still in progress. Continue working toward the goal, call update_goal with status wait_for_user when useful progress requires new user or external input, or call update_goal with status success or failure when the goal is complete or cannot be completed."
	if contract, ok := state.RenderProviderContext(); ok {
		prompt = "The current thread goal is still in progress.\n\n" + contract +
			"\n\nContinue working, call update_goal with status wait_for_user when useful progress requires new user or external input, or call update_goal with status success or failure when the goal is complete or cannot be completed."
	}
	return GoalGateDecision{
		Status:            state.Status,
		BlockStop:         true,
		ContinuePrompt:    prompt,
		Reason:            "goal_in_progress",
		ContinuationCount: state.ContinuationCount,
	}, nil
}

func (s *GoalStateStore) RecordContinuation(decision GoalGateDecision) (bool, error) {
	if s == nil || !decision.BlockStop {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	if state.StatusSnapshot() == nil || state.Status != GoalStatusInProgress {
		return false, nil
	}
	state.ContinuationCount++
	state.UpdatedAt = s.now()
	if err := s.saveLocked(state); err != nil {
		return false, err
	}
	return true, nil
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
	return decodeGoalState(data)
}

func decodeGoalState(data []byte) (GoalState, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state GoalState
	if err := decoder.Decode(&state); err != nil {
		return GoalState{Version: 1}, fmt.Errorf("goal state parse: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return GoalState{Version: 1}, fmt.Errorf("goal state parse: %w", err)
	}
	if state.Version != 1 {
		return GoalState{Version: 1}, fmt.Errorf("goal state: unsupported version %d", state.Version)
	}
	if state.ContinuationCount < 0 {
		return GoalState{Version: 1}, fmt.Errorf("goal state: continuation_count cannot be negative")
	}
	if state.Status != "" {
		if err := validateGoalStatus(state.Status); err != nil {
			return GoalState{Version: 1}, fmt.Errorf("goal state: %w", err)
		}
	}
	state = normalizeGoalState(state)
	if state.present() && strings.TrimSpace(state.Description) == "" {
		return GoalState{Version: 1}, fmt.Errorf("goal state: description is required")
	}
	if state.present() && state.UpdatedAt.IsZero() {
		return GoalState{Version: 1}, fmt.Errorf("goal state: updated_at is required")
	}
	return state, nil
}

func (s *GoalStateStore) saveLocked(state GoalState) error {
	if s == nil || s.Path == "" {
		return nil
	}
	state = normalizeGoalState(state)
	data, err := json.MarshalIndent(redactGoalState(state), "", "  ")
	if err != nil {
		return fmt.Errorf("goal state encode: %w", err)
	}
	data = append(data, '\n')
	if err := replaceFileAtomic(s.Path, data, 0o600); err != nil {
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
	state.Acceptance = sanitizeGoalAcceptance(state.Acceptance)
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
	case GoalStatusInProgress, GoalStatusWaitForUser, GoalStatusSuccess, GoalStatusFailure:
		return nil
	default:
		return fmt.Errorf("invalid goal status %q", status)
	}
}

func redactGoalState(state GoalState) GoalState {
	state.Description = sanitizeGoalText(state.Description)
	state.Acceptance = sanitizeGoalAcceptance(state.Acceptance)
	state.StatusReason = sanitizeGoalText(state.StatusReason)
	return state
}

func sanitizeGoalText(text string) string {
	return sanitizeWorkmemText(text, 1000)
}

func sanitizeGoalAcceptance(text string) string {
	return sanitizeWorkmemText(text, maxGoalAcceptanceBytes)
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
