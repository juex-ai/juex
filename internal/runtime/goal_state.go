package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

const goalStateFile = "goal_state.json"

type GoalStatus string

const (
	GoalStatusInProgress GoalStatus = "in_progress"
	GoalStatusSuccess    GoalStatus = "success"
	GoalStatusFailure    GoalStatus = "failure"
)

type GoalState struct {
	Version            int        `json:"version"`
	Description        string     `json:"description,omitempty"`
	VerificationMethod string     `json:"verification_method,omitempty"`
	ContinuationCount  int        `json:"continuation_count,omitempty"`
	Status             GoalStatus `json:"status,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at,omitempty"`
}

type GoalStateUpdate struct {
	Description        *string    `json:"description,omitempty"`
	VerificationMethod *string    `json:"verification_method,omitempty"`
	Status             GoalStatus `json:"status,omitempty"`
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
	Description        string     `json:"description,omitempty"`
	VerificationMethod string     `json:"verification_method,omitempty"`
	ContinuationCount  int        `json:"continuation_count,omitempty"`
	Status             GoalStatus `json:"status,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at,omitempty"`
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

func (s *GoalStateStore) Create(description, verificationMethod string) (GoalState, error) {
	if s == nil {
		return GoalState{Version: 1}, nil
	}
	description = sanitizeGoalText(description)
	verificationMethod = sanitizeGoalText(verificationMethod)
	if description == "" {
		return GoalState{}, fmt.Errorf("goal description is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := GoalState{
		Version:            1,
		Description:        description,
		VerificationMethod: verificationMethod,
		ContinuationCount:  0,
		Status:             GoalStatusInProgress,
		UpdatedAt:          s.now(),
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
	if update.VerificationMethod != nil {
		state.VerificationMethod = sanitizeGoalText(*update.VerificationMethod)
	}
	if update.Status != "" {
		if err := validateGoalStatus(update.Status); err != nil {
			return GoalState{}, err
		}
		state.Status = update.Status
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
	if state.Description != "" || state.VerificationMethod != "" {
		prompt = "The current session goal is still in progress.\n\nGoal: " + state.Description
		if state.VerificationMethod != "" {
			prompt += "\nVerification: " + state.VerificationMethod
		}
		prompt += "\n\nContinue working, or call update_goal with status success or failure when the goal is complete or cannot be completed."
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
	if strings.TrimSpace(s.Description) == "" && s.Status == "" {
		return nil
	}
	return &GoalStatusSnapshot{
		Description:        s.Description,
		VerificationMethod: s.VerificationMethod,
		ContinuationCount:  s.ContinuationCount,
		Status:             s.Status,
		UpdatedAt:          s.UpdatedAt,
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
	var legacy legacyGoalState
	if err := json.Unmarshal(data, &legacy); err == nil && shouldNormalizeLegacyGoalState(state, legacy) {
		state = legacy.normalize()
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
	state.VerificationMethod = sanitizeGoalText(state.VerificationMethod)
	if state.ContinuationCount < 0 {
		state.ContinuationCount = 0
	}
	if state.Description != "" || state.Status != "" {
		if state.Status == "" {
			state.Status = GoalStatusInProgress
		}
		if err := validateGoalStatus(state.Status); err != nil {
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

func shouldNormalizeLegacyGoalState(state GoalState, legacy legacyGoalState) bool {
	return (state.Description == "" && legacy.Objective != "") || isLegacyGoalStatus(legacy.Status)
}

func isLegacyGoalStatus(status GoalStatus) bool {
	switch status {
	case "continue", "blocked", "complete", "unchecked":
		return true
	default:
		return false
	}
}

type legacyGoalState struct {
	Version            int        `json:"version"`
	Objective          string     `json:"objective,omitempty"`
	Description        string     `json:"description,omitempty"`
	VerificationMethod string     `json:"verification_method,omitempty"`
	Status             GoalStatus `json:"status,omitempty"`
	ContinuationCount  int        `json:"continuation_count,omitempty"`
	Budget             struct {
		ContinuationsUsed int `json:"continuations_used,omitempty"`
	} `json:"budget,omitempty"`
	LastProgress string    `json:"last_progress,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

func (s legacyGoalState) normalize() GoalState {
	description := s.Description
	if description == "" {
		description = s.Objective
	}
	status := s.Status
	switch status {
	case "complete":
		status = GoalStatusSuccess
	case "blocked":
		status = GoalStatusFailure
	case "continue", "unchecked":
		status = GoalStatusInProgress
	}
	count := s.ContinuationCount
	if count == 0 {
		count = s.Budget.ContinuationsUsed
	}
	return GoalState{
		Version:            1,
		Description:        description,
		VerificationMethod: s.VerificationMethod,
		ContinuationCount:  count,
		Status:             status,
		UpdatedAt:          s.UpdatedAt,
	}
}

func (e *Engine) goalStateStoreLocked() *GoalStateStore {
	if e == nil {
		return nil
	}
	return e.GoalState
}

func (e *Engine) goalStateRawLocked() (json.RawMessage, bool) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return nil, false
	}
	state, err := store.Snapshot()
	if err != nil {
		return nil, false
	}
	raw := state.RawMessage()
	if len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

func (e *Engine) GoalStatusSnapshot() (*GoalStatusSnapshot, error) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (e *Engine) emitGoalUpdated(turnID string) {
	if e == nil {
		return
	}
	store := e.goalStateStoreLocked()
	if store == nil {
		return
	}
	snapshot, err := store.StatusSnapshot()
	if err != nil || snapshot == nil {
		return
	}
	e.emit(events.Event{Type: "goal.updated", TurnID: turnID, Payload: goalUpdatedPayload(snapshot)})
}

func goalUpdatedPayload(snapshot *GoalStatusSnapshot) GoalUpdatedPayload {
	if snapshot == nil {
		return GoalUpdatedPayload{}
	}
	return GoalUpdatedPayload{
		Description:        snapshot.Description,
		VerificationMethod: snapshot.VerificationMethod,
		ContinuationCount:  snapshot.ContinuationCount,
		Status:             snapshot.Status,
	}
}

func goalContinuedPayload(decision GoalGateDecision, snapshot *GoalStatusSnapshot) GoalContinuedPayload {
	count := decision.ContinuationCount
	if snapshot != nil {
		count = snapshot.ContinuationCount
	}
	return GoalContinuedPayload{
		Status:                decision.Status,
		Reason:                decision.Reason,
		ContinuationCount:     count,
		ContinuationPromptLen: len(decision.ContinuePrompt),
	}
}

func redactGoalState(state GoalState) GoalState {
	state.Description = sanitizeGoalText(state.Description)
	state.VerificationMethod = sanitizeGoalText(state.VerificationMethod)
	return state
}

func sanitizeGoalText(text string) string {
	return sanitizeWorkingStateText(text)
}
