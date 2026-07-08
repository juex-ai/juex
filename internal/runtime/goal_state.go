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
	Version                int        `json:"version"`
	Description            string     `json:"description,omitempty"`
	AcceptanceCriteria     []string   `json:"acceptance_criteria,omitempty"`
	RequiredArtifacts      []string   `json:"required_artifacts,omitempty"`
	ArtifactRequirements   []string   `json:"artifact_requirements,omitempty"`
	ValidationRequirements []string   `json:"validation_requirements,omitempty"`
	VerificationMethod     string     `json:"verification_method,omitempty"`
	ContinuationCount      int        `json:"continuation_count,omitempty"`
	Status                 GoalStatus `json:"status,omitempty"`
	StatusReason           string     `json:"status_reason,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at,omitempty"`
}

type GoalStateUpdate struct {
	Description            *string    `json:"description,omitempty"`
	AcceptanceCriteria     *[]string  `json:"acceptance_criteria,omitempty"`
	RequiredArtifacts      *[]string  `json:"required_artifacts,omitempty"`
	ArtifactRequirements   *[]string  `json:"artifact_requirements,omitempty"`
	ValidationRequirements *[]string  `json:"validation_requirements,omitempty"`
	VerificationMethod     *string    `json:"verification_method,omitempty"`
	Status                 GoalStatus `json:"status,omitempty"`
	StatusReason           *string    `json:"status_reason,omitempty"`
}

type GoalStateCreate struct {
	Description            string   `json:"description,omitempty"`
	AcceptanceCriteria     []string `json:"acceptance_criteria,omitempty"`
	RequiredArtifacts      []string `json:"required_artifacts,omitempty"`
	ArtifactRequirements   []string `json:"artifact_requirements,omitempty"`
	ValidationRequirements []string `json:"validation_requirements,omitempty"`
	VerificationMethod     string   `json:"verification_method,omitempty"`
	StatusReason           string   `json:"status_reason,omitempty"`
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
	Description            string     `json:"description,omitempty"`
	AcceptanceCriteria     []string   `json:"acceptance_criteria,omitempty"`
	RequiredArtifacts      []string   `json:"required_artifacts,omitempty"`
	ArtifactRequirements   []string   `json:"artifact_requirements,omitempty"`
	ValidationRequirements []string   `json:"validation_requirements,omitempty"`
	VerificationMethod     string     `json:"verification_method,omitempty"`
	ContinuationCount      int        `json:"continuation_count,omitempty"`
	Status                 GoalStatus `json:"status,omitempty"`
	StatusReason           string     `json:"status_reason,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at,omitempty"`
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
	return s.CreateWithContract(GoalStateCreate{
		Description:        description,
		VerificationMethod: verificationMethod,
	})
}

func (s *GoalStateStore) CreateWithContract(create GoalStateCreate) (GoalState, error) {
	if s == nil {
		return GoalState{Version: 1}, nil
	}
	description := sanitizeGoalText(create.Description)
	verificationMethod := sanitizeGoalText(create.VerificationMethod)
	if description == "" {
		return GoalState{}, fmt.Errorf("goal description is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := GoalState{
		Version:                1,
		Description:            description,
		AcceptanceCriteria:     sanitizeGoalTextList(create.AcceptanceCriteria),
		RequiredArtifacts:      sanitizeGoalTextList(create.RequiredArtifacts),
		ArtifactRequirements:   sanitizeGoalTextList(create.ArtifactRequirements),
		ValidationRequirements: sanitizeGoalTextList(create.ValidationRequirements),
		VerificationMethod:     verificationMethod,
		ContinuationCount:      0,
		Status:                 GoalStatusInProgress,
		StatusReason:           sanitizeGoalText(create.StatusReason),
		UpdatedAt:              s.now(),
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
	if update.AcceptanceCriteria != nil {
		state.AcceptanceCriteria = sanitizeGoalTextList(*update.AcceptanceCriteria)
	}
	if update.RequiredArtifacts != nil {
		state.RequiredArtifacts = sanitizeGoalTextList(*update.RequiredArtifacts)
	}
	if update.ArtifactRequirements != nil {
		state.ArtifactRequirements = sanitizeGoalTextList(*update.ArtifactRequirements)
	}
	if update.ValidationRequirements != nil {
		state.ValidationRequirements = sanitizeGoalTextList(*update.ValidationRequirements)
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
		Description:            s.Description,
		AcceptanceCriteria:     append([]string(nil), s.AcceptanceCriteria...),
		RequiredArtifacts:      append([]string(nil), s.RequiredArtifacts...),
		ArtifactRequirements:   append([]string(nil), s.ArtifactRequirements...),
		ValidationRequirements: append([]string(nil), s.ValidationRequirements...),
		VerificationMethod:     s.VerificationMethod,
		ContinuationCount:      s.ContinuationCount,
		Status:                 s.Status,
		StatusReason:           s.StatusReason,
		UpdatedAt:              s.UpdatedAt,
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
	state.AcceptanceCriteria = sanitizeGoalTextList(state.AcceptanceCriteria)
	state.RequiredArtifacts = sanitizeGoalTextList(state.RequiredArtifacts)
	state.ArtifactRequirements = sanitizeGoalTextList(state.ArtifactRequirements)
	state.ValidationRequirements = sanitizeGoalTextList(state.ValidationRequirements)
	state.StatusReason = sanitizeGoalText(state.StatusReason)
	if state.ContinuationCount < 0 {
		state.ContinuationCount = 0
	}
	if state.present() {
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

func (e *Engine) goalStateContextSnapshot() (string, bool) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (e *Engine) goalStateContextLocked() (string, bool) {
	return e.goalStateContextSnapshot()
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
		Description:            snapshot.Description,
		AcceptanceCriteria:     append([]string(nil), snapshot.AcceptanceCriteria...),
		RequiredArtifacts:      append([]string(nil), snapshot.RequiredArtifacts...),
		ArtifactRequirements:   append([]string(nil), snapshot.ArtifactRequirements...),
		ValidationRequirements: append([]string(nil), snapshot.ValidationRequirements...),
		VerificationMethod:     snapshot.VerificationMethod,
		ContinuationCount:      snapshot.ContinuationCount,
		Status:                 snapshot.Status,
		StatusReason:           snapshot.StatusReason,
		UpdatedAt:              snapshot.UpdatedAt,
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
	state.AcceptanceCriteria = sanitizeGoalTextList(state.AcceptanceCriteria)
	state.RequiredArtifacts = sanitizeGoalTextList(state.RequiredArtifacts)
	state.ArtifactRequirements = sanitizeGoalTextList(state.ArtifactRequirements)
	state.ValidationRequirements = sanitizeGoalTextList(state.ValidationRequirements)
	state.StatusReason = sanitizeGoalText(state.StatusReason)
	return state
}

func sanitizeGoalText(text string) string {
	return sanitizeWorkingStateText(text)
}

func sanitizeGoalTextList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	const maxGoalListItems = 24
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = sanitizeGoalText(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) >= maxGoalListItems {
			break
		}
	}
	return out
}

func (s GoalState) present() bool {
	return strings.TrimSpace(s.Description) != "" ||
		len(s.AcceptanceCriteria) > 0 ||
		len(s.RequiredArtifacts) > 0 ||
		len(s.ArtifactRequirements) > 0 ||
		len(s.ValidationRequirements) > 0 ||
		strings.TrimSpace(s.VerificationMethod) != "" ||
		s.Status != "" ||
		strings.TrimSpace(s.StatusReason) != ""
}

func (s GoalState) RenderProviderContext() (string, bool) {
	if !s.present() {
		return "", false
	}
	var b strings.Builder
	b.WriteString("Current goal contract (model-owned; update with update_goal when the contract or evidence changes):\n")
	writeGoalContextValue(&b, "description", s.Description)
	writeGoalContextList(&b, "acceptance criteria", s.AcceptanceCriteria)
	writeGoalContextList(&b, "required artifacts", s.RequiredArtifacts)
	writeGoalContextList(&b, "artifact requirements", s.ArtifactRequirements)
	writeGoalContextList(&b, "validation requirements", s.ValidationRequirements)
	writeGoalContextValue(&b, "verification", s.VerificationMethod)
	if s.Status != "" {
		writeGoalContextValue(&b, "status", string(s.Status))
	}
	writeGoalContextValue(&b, "status reason", s.StatusReason)
	return strings.TrimRight(b.String(), "\n"), true
}

func writeGoalContextValue(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", label, value)
}

func writeGoalContextList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "- %s:\n", label)
	for i, value := range values {
		if i >= 12 {
			fmt.Fprintf(b, "  - %d additional item(s) omitted\n", len(values)-i)
			break
		}
		fmt.Fprintf(b, "  - %s\n", value)
	}
}
