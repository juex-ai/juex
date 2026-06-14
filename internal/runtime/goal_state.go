package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

const (
	goalStateFile               = "goal_state.json"
	DefaultGoalMaxContinuations = 3
)

type GoalStatus string

const (
	GoalStatusInProgress GoalStatus = "in_progress"
	GoalStatusContinue   GoalStatus = "continue"
	GoalStatusBlocked    GoalStatus = "blocked"
	GoalStatusComplete   GoalStatus = "complete"
	GoalStatusUnchecked  GoalStatus = "unchecked"
)

type GoalState struct {
	Version       int              `json:"version"`
	Objective     string           `json:"objective,omitempty"`
	Status        GoalStatus       `json:"status,omitempty"`
	Evidence      []GoalEvidence   `json:"evidence,omitempty"`
	Budget        GoalBudget       `json:"budget,omitempty"`
	BlockedReason string           `json:"blocked_reason,omitempty"`
	NextUserInput string           `json:"next_user_input,omitempty"`
	LastProgress  string           `json:"last_progress,omitempty"`
	LastCheck     *CompletionCheck `json:"last_check,omitempty"`
	UpdatedAt     time.Time        `json:"updated_at,omitempty"`
}

type GoalEvidence struct {
	ID           string    `json:"id,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	Text         string    `json:"text,omitempty"`
	Source       string    `json:"source,omitempty"`
	RelatedPaths []string  `json:"related_paths,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

type GoalBudget struct {
	MaxContinuations  int `json:"max_continuations,omitempty"`
	ContinuationsUsed int `json:"continuations_used,omitempty"`
}

type CompletionCheck struct {
	Status         GoalStatus `json:"status,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	ContinuePrompt string     `json:"continue_prompt,omitempty"`
	Source         string     `json:"source,omitempty"`
	CheckedAt      time.Time  `json:"checked_at,omitempty"`
}

type GoalStatePatch struct {
	Objective       string           `json:"objective,omitempty"`
	Status          GoalStatus       `json:"status,omitempty"`
	Evidence        []GoalEvidence   `json:"evidence,omitempty"`
	Budget          *GoalBudget      `json:"budget,omitempty"`
	BlockedReason   string           `json:"blocked_reason,omitempty"`
	NextUserInput   string           `json:"next_user_input,omitempty"`
	LastProgress    string           `json:"last_progress,omitempty"`
	CompletionCheck *CompletionCheck `json:"completion_check,omitempty"`
	ContinuePrompt  string           `json:"continue_prompt,omitempty"`
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
	ContinuationsUsed int        `json:"continuations_used,omitempty"`
	MaxContinuations  int        `json:"max_continuations,omitempty"`
}

type GoalStatusSnapshot struct {
	Objective     string           `json:"objective,omitempty"`
	Status        GoalStatus       `json:"status,omitempty"`
	Evidence      []GoalEvidence   `json:"evidence,omitempty"`
	Budget        GoalBudget       `json:"budget,omitempty"`
	BlockedReason string           `json:"blocked_reason,omitempty"`
	NextUserInput string           `json:"next_user_input,omitempty"`
	LastProgress  string           `json:"last_progress,omitempty"`
	LastCheck     *CompletionCheck `json:"last_check,omitempty"`
	UpdatedAt     time.Time        `json:"updated_at,omitempty"`
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

func (s *GoalStateStore) BeginTurn(objective string) error {
	if s == nil {
		return nil
	}
	objective = sanitizeGoalText(strings.TrimSpace(objective))
	if objective == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	if state.Objective != "" && state.Status != GoalStatusComplete && state.Status != GoalStatusBlocked && state.Status != GoalStatusUnchecked {
		return nil
	}
	now := s.now()
	state.Objective = objective
	state.Status = GoalStatusInProgress
	state.Budget = defaultGoalBudget(state.Budget)
	state.BlockedReason = ""
	state.NextUserInput = ""
	state.LastProgress = "turn started"
	state.LastCheck = nil
	state.UpdatedAt = now
	return s.saveLocked(state)
}

func (s *GoalStateStore) ApplyPatch(patch GoalStatePatch) error {
	if s == nil || patch.empty() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	now := s.now()
	state.Budget = defaultGoalBudget(state.Budget)
	if patch.Objective != "" {
		state.Objective = sanitizeGoalText(patch.Objective)
	}
	if patch.Status != "" {
		state.Status = patch.Status
	}
	if patch.Budget != nil {
		if patch.Budget.MaxContinuations > 0 {
			state.Budget.MaxContinuations = patch.Budget.MaxContinuations
		}
		if patch.Budget.ContinuationsUsed >= 0 {
			state.Budget.ContinuationsUsed = patch.Budget.ContinuationsUsed
		}
	}
	if patch.LastProgress != "" {
		state.LastProgress = sanitizeGoalText(patch.LastProgress)
	}
	switch patch.Status {
	case GoalStatusBlocked:
		state.BlockedReason = sanitizeGoalText(patch.BlockedReason)
		state.NextUserInput = sanitizeGoalText(patch.NextUserInput)
	case GoalStatusComplete:
		state.BlockedReason = ""
		state.NextUserInput = ""
	default:
		if patch.BlockedReason != "" {
			state.BlockedReason = sanitizeGoalText(patch.BlockedReason)
		}
		if patch.NextUserInput != "" {
			state.NextUserInput = sanitizeGoalText(patch.NextUserInput)
		}
	}
	if len(patch.Evidence) > 0 {
		state.Evidence = mergeGoalEvidence(state.Evidence, patch.Evidence, now)
	}
	if patch.CompletionCheck != nil || patch.ContinuePrompt != "" || patch.Status == GoalStatusContinue || patch.Status == GoalStatusComplete || patch.Status == GoalStatusBlocked {
		check := CompletionCheck{Status: patch.Status, ContinuePrompt: patch.ContinuePrompt}
		if patch.CompletionCheck != nil {
			check = *patch.CompletionCheck
			if check.ContinuePrompt == "" {
				check.ContinuePrompt = patch.ContinuePrompt
			}
			if check.Status == "" {
				check.Status = patch.Status
			}
		}
		check = normalizeCompletionCheck(check, now)
		if check.Status != "" || check.Summary != "" || check.ContinuePrompt != "" {
			state.LastCheck = &check
			if patch.Status == "" && check.Status != "" {
				state.Status = check.Status
			}
		}
	}
	if state.Status == "" {
		state.Status = GoalStatusInProgress
	}
	state.UpdatedAt = now
	return s.saveLocked(state)
}

func (s *GoalStateStore) MarkUnchecked() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	if state.Objective == "" || state.Status != GoalStatusInProgress {
		return nil
	}
	now := s.now()
	state.Status = GoalStatusUnchecked
	state.LastCheck = &CompletionCheck{
		Status:    GoalStatusUnchecked,
		Summary:   "no completion check reported goal status",
		Source:    "runtime",
		CheckedAt: now,
	}
	state.UpdatedAt = now
	return s.saveLocked(state)
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
	state.Budget = defaultGoalBudget(state.Budget)
	status := state.Status
	if status == "" && state.LastCheck != nil {
		status = state.LastCheck.Status
	}
	decision := GoalGateDecision{
		Status:            status,
		ContinuationsUsed: state.Budget.ContinuationsUsed,
		MaxContinuations:  state.Budget.MaxContinuations,
	}
	switch status {
	case GoalStatusContinue:
		if state.Budget.MaxContinuations > 0 && state.Budget.ContinuationsUsed >= state.Budget.MaxContinuations {
			decision.Status = GoalStatusBlocked
			decision.Reason = "continuation_budget_exhausted"
			return decision, nil
		}
		prompt := ""
		if state.LastCheck != nil {
			prompt = strings.TrimSpace(state.LastCheck.ContinuePrompt)
		}
		if prompt == "" {
			return GoalGateDecision{}, fmt.Errorf("goal state: continue status requires completion_check.continue_prompt")
		}
		decision.BlockStop = true
		decision.ContinuePrompt = prompt
		decision.Reason = "completion_check_continue"
		return decision, nil
	case GoalStatusBlocked:
		if strings.TrimSpace(state.BlockedReason) != "" && strings.TrimSpace(state.NextUserInput) != "" {
			return decision, nil
		}
		if state.Budget.MaxContinuations > 0 && state.Budget.ContinuationsUsed >= state.Budget.MaxContinuations {
			decision.Reason = "blocked_details_missing_budget_exhausted"
			return decision, nil
		}
		decision.BlockStop = true
		decision.Reason = "blocked_details_missing"
		decision.ContinuePrompt = "Runtime observation: completion check marked the goal blocked, but goal_state.blocked_reason and goal_state.next_user_input are not both set. Do not continue ordinary work. Explain the concrete blocker and the exact next user input needed before finishing."
		return decision, nil
	default:
		return decision, nil
	}
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
	state.Budget = defaultGoalBudget(state.Budget)
	state.Budget.ContinuationsUsed++
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
	if strings.TrimSpace(s.Objective) == "" && s.Status == "" && s.LastCheck == nil {
		return nil
	}
	s.Evidence = append([]GoalEvidence(nil), s.Evidence...)
	if s.LastCheck != nil {
		check := *s.LastCheck
		s.LastCheck = &check
	}
	return &GoalStatusSnapshot{
		Objective:     s.Objective,
		Status:        s.Status,
		Evidence:      s.Evidence,
		Budget:        s.Budget,
		BlockedReason: s.BlockedReason,
		NextUserInput: s.NextUserInput,
		LastProgress:  s.LastProgress,
		LastCheck:     s.LastCheck,
		UpdatedAt:     s.UpdatedAt,
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
	state := GoalState{Version: 1, Budget: GoalBudget{MaxContinuations: DefaultGoalMaxContinuations}}
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
	if len(strings.TrimSpace(string(data))) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("goal state parse: %w", err)
	}
	if state.Version == 0 {
		state.Version = 1
	}
	state.Budget = defaultGoalBudget(state.Budget)
	return state, nil
}

func (s *GoalStateStore) saveLocked(state GoalState) error {
	if s == nil || s.Path == "" {
		return nil
	}
	state.Version = 1
	state.Budget = defaultGoalBudget(state.Budget)
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

func (p GoalStatePatch) empty() bool {
	return strings.TrimSpace(p.Objective) == "" &&
		p.Status == "" &&
		len(p.Evidence) == 0 &&
		p.Budget == nil &&
		strings.TrimSpace(p.BlockedReason) == "" &&
		strings.TrimSpace(p.NextUserInput) == "" &&
		strings.TrimSpace(p.LastProgress) == "" &&
		p.CompletionCheck == nil &&
		strings.TrimSpace(p.ContinuePrompt) == ""
}

func defaultGoalBudget(b GoalBudget) GoalBudget {
	if b.MaxContinuations <= 0 {
		b.MaxContinuations = DefaultGoalMaxContinuations
	}
	if b.ContinuationsUsed < 0 {
		b.ContinuationsUsed = 0
	}
	return b
}

func mergeGoalEvidence(existing, incoming []GoalEvidence, now time.Time) []GoalEvidence {
	out := append([]GoalEvidence(nil), existing...)
	index := map[string]int{}
	for i, item := range out {
		if item.ID != "" {
			index[item.ID] = i
		}
	}
	for _, item := range incoming {
		item = normalizeGoalEvidence(item, now)
		if item.ID == "" || item.Text == "" {
			continue
		}
		if i, ok := index[item.ID]; ok {
			out[i] = item
			continue
		}
		index[item.ID] = len(out)
		out = append(out, item)
	}
	return out
}

func normalizeGoalEvidence(item GoalEvidence, now time.Time) GoalEvidence {
	item.Kind = sanitizeGoalText(item.Kind)
	item.Text = sanitizeGoalText(item.Text)
	item.Source = sanitizeGoalText(item.Source)
	item.RelatedPaths = normalizeWorkingStatePaths(item.RelatedPaths)
	if item.CreatedAt.IsZero() && item.Text != "" {
		item.CreatedAt = now
	}
	if strings.TrimSpace(item.ID) == "" && item.Text != "" {
		item.ID = goalEvidenceID(item)
	}
	return item
}

func normalizeCompletionCheck(check CompletionCheck, now time.Time) CompletionCheck {
	check.Summary = sanitizeGoalText(check.Summary)
	check.ContinuePrompt = sanitizeGoalText(check.ContinuePrompt)
	check.Source = sanitizeGoalText(check.Source)
	if check.CheckedAt.IsZero() {
		check.CheckedAt = now
	}
	return check
}

func goalEvidenceID(item GoalEvidence) string {
	body := item.Kind + "\x00" + item.Text + "\x00" + strings.Join(item.RelatedPaths, "\x00")
	sum := sha256.Sum256([]byte(body))
	return "evidence-" + hex.EncodeToString(sum[:6])
}

func (e *Engine) goalStateStoreLocked() *GoalStateStore {
	if e == nil {
		return nil
	}
	if e.GoalState != nil {
		return e.GoalState
	}
	if e.Session == nil || e.Session.Dir == "" {
		return nil
	}
	e.GoalState = NewGoalStateStore(e.Session.Dir, GoalStateOptions{})
	return e.GoalState
}

func (e *Engine) beginGoalTurnLocked(turnID, objective string) error {
	store := e.goalStateStoreLocked()
	if store == nil {
		return nil
	}
	if err := store.BeginTurn(objective); err != nil {
		return err
	}
	e.emitGoalUpdated(turnID)
	return nil
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
	var check *CompletionCheck
	if snapshot.LastCheck != nil {
		copied := *snapshot.LastCheck
		check = &copied
	}
	return GoalUpdatedPayload{
		Objective:     snapshot.Objective,
		Status:        snapshot.Status,
		LastProgress:  snapshot.LastProgress,
		BlockedReason: snapshot.BlockedReason,
		NextUserInput: snapshot.NextUserInput,
		LastCheck:     check,
	}
}

func goalContinuedPayload(decision GoalGateDecision, snapshot *GoalStatusSnapshot) GoalContinuedPayload {
	used := decision.ContinuationsUsed
	max := decision.MaxContinuations
	if snapshot != nil {
		used = snapshot.Budget.ContinuationsUsed
		max = snapshot.Budget.MaxContinuations
	}
	return GoalContinuedPayload{
		Status:                decision.Status,
		Reason:                decision.Reason,
		ContinuationsUsed:     used,
		MaxContinuations:      max,
		ContinuationPromptLen: len(decision.ContinuePrompt),
	}
}

func redactGoalState(state GoalState) GoalState {
	state.Objective = sanitizeGoalText(state.Objective)
	state.BlockedReason = sanitizeGoalText(state.BlockedReason)
	state.NextUserInput = sanitizeGoalText(state.NextUserInput)
	state.LastProgress = sanitizeGoalText(state.LastProgress)
	for i := range state.Evidence {
		state.Evidence[i] = normalizeGoalEvidence(state.Evidence[i], state.Evidence[i].CreatedAt)
	}
	if state.LastCheck != nil {
		check := normalizeCompletionCheck(*state.LastCheck, state.LastCheck.CheckedAt)
		state.LastCheck = &check
	}
	return state
}

func sanitizeGoalText(text string) string {
	return sanitizeWorkingStateText(text)
}
