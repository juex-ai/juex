package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

const workingStateFile = "working_state.json"

type WorkingStateSource string

const (
	WorkingStateSourceModelSummary   WorkingStateSource = "model_summary"
	WorkingStateSourceToolResult     WorkingStateSource = "tool_result"
	WorkingStateSourceHookExtraction WorkingStateSource = "hook_extraction"
	WorkingStateSourceUserInput      WorkingStateSource = "user_input"
)

type WorkingStateSeverity string

const (
	WorkingStateSeverityLow      WorkingStateSeverity = "low"
	WorkingStateSeverityMedium   WorkingStateSeverity = "medium"
	WorkingStateSeverityHigh     WorkingStateSeverity = "high"
	WorkingStateSeverityCritical WorkingStateSeverity = "critical"
)

type WorkingStateRecord struct {
	ID           string               `json:"id,omitempty"`
	Text         string               `json:"text,omitempty"`
	Source       WorkingStateSource   `json:"source,omitempty"`
	Confidence   float64              `json:"confidence,omitempty"`
	Severity     WorkingStateSeverity `json:"severity,omitempty"`
	RelatedPaths []string             `json:"related_paths,omitempty"`
	CreatedAt    time.Time            `json:"created_at,omitempty"`
	ResolvedAt   time.Time            `json:"resolved_at,omitempty"`
}

func (r WorkingStateRecord) MarshalJSON() ([]byte, error) {
	type recordJSON struct {
		ID           string               `json:"id,omitempty"`
		Text         string               `json:"text,omitempty"`
		Source       WorkingStateSource   `json:"source,omitempty"`
		Confidence   float64              `json:"confidence,omitempty"`
		Severity     WorkingStateSeverity `json:"severity,omitempty"`
		RelatedPaths []string             `json:"related_paths,omitempty"`
		CreatedAt    *time.Time           `json:"created_at,omitempty"`
		ResolvedAt   *time.Time           `json:"resolved_at,omitempty"`
	}
	out := recordJSON{
		ID:           r.ID,
		Text:         r.Text,
		Source:       r.Source,
		Confidence:   r.Confidence,
		Severity:     r.Severity,
		RelatedPaths: r.RelatedPaths,
	}
	if !r.CreatedAt.IsZero() {
		created := r.CreatedAt
		out.CreatedAt = &created
	}
	if !r.ResolvedAt.IsZero() {
		resolved := r.ResolvedAt
		out.ResolvedAt = &resolved
	}
	return json.Marshal(out)
}

func (r *WorkingStateRecord) UnmarshalJSON(data []byte) error {
	type recordJSON struct {
		ID           string               `json:"id,omitempty"`
		Text         string               `json:"text,omitempty"`
		Source       WorkingStateSource   `json:"source,omitempty"`
		Confidence   float64              `json:"confidence,omitempty"`
		Severity     WorkingStateSeverity `json:"severity,omitempty"`
		RelatedPaths []string             `json:"related_paths,omitempty"`
		CreatedAt    *time.Time           `json:"created_at,omitempty"`
		ResolvedAt   *time.Time           `json:"resolved_at,omitempty"`
	}
	var in recordJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	r.ID = in.ID
	r.Text = in.Text
	r.Source = in.Source
	r.Confidence = in.Confidence
	r.Severity = in.Severity
	r.RelatedPaths = in.RelatedPaths
	if in.CreatedAt != nil {
		r.CreatedAt = *in.CreatedAt
	} else {
		r.CreatedAt = time.Time{}
	}
	if in.ResolvedAt != nil {
		r.ResolvedAt = *in.ResolvedAt
	} else {
		r.ResolvedAt = time.Time{}
	}
	return nil
}

type WorkingState struct {
	Version              int                  `json:"version"`
	UpdatedAt            time.Time            `json:"updated_at,omitempty"`
	Goal                 *WorkingStateRecord  `json:"goal,omitempty"`
	HardConstraints      []WorkingStateRecord `json:"hard_constraints,omitempty"`
	Artifacts            []WorkingStateRecord `json:"artifacts,omitempty"`
	Checks               []WorkingStateRecord `json:"checks,omitempty"`
	OpenIssues           []WorkingStateRecord `json:"open_issues,omitempty"`
	LastSuccessfulChecks []WorkingStateRecord `json:"last_successful_checks,omitempty"`
	StaleChecks          []WorkingStateRecord `json:"stale_checks,omitempty"`
}

type WorkingStateStatusSnapshot struct {
	Path     string       `json:"path,omitempty"`
	Disabled bool         `json:"disabled,omitempty"`
	Present  bool         `json:"present"`
	State    WorkingState `json:"state"`
}

type WorkingStatePatch struct {
	Goal                 *WorkingStateRecord  `json:"goal,omitempty"`
	HardConstraints      []WorkingStateRecord `json:"hard_constraints,omitempty"`
	Artifacts            []WorkingStateRecord `json:"artifacts,omitempty"`
	Checks               []WorkingStateRecord `json:"checks,omitempty"`
	OpenIssues           []WorkingStateRecord `json:"open_issues,omitempty"`
	LastSuccessfulChecks []WorkingStateRecord `json:"last_successful_checks,omitempty"`
	StaleChecks          []WorkingStateRecord `json:"stale_checks,omitempty"`
}

type WorkingStateOptions struct {
	Now func() time.Time
}

type WorkingStateStore struct {
	SessionDir string
	Path       string
	Now        func() time.Time

	mu sync.Mutex
}

type WorkingStateCheckObservation struct {
	ToolName     string
	ToolUseID    string
	Text         string
	RelatedPaths []string
}

type WorkingStateIssueObservation struct {
	ToolName     string
	ToolUseID    string
	Text         string
	Severity     WorkingStateSeverity
	Confidence   float64
	RelatedPaths []string
}

func NewWorkingStateStore(sessionDir string, opts WorkingStateOptions) *WorkingStateStore {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &WorkingStateStore{
		SessionDir: sessionDir,
		Path:       filepath.Join(sessionDir, workingStateFile),
		Now:        now,
	}
}

func (s *WorkingStateStore) Snapshot() (WorkingState, error) {
	if s == nil {
		return WorkingState{Version: 1}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *WorkingStateStore) ApplyPatch(patch WorkingStatePatch) error {
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
	changed := false
	if patch.Goal != nil {
		rec := normalizeWorkingStateRecord("goal", *patch.Goal, now, "")
		if rec.ID != "" {
			if state.Goal == nil {
				if rec.Text != "" {
					state.Goal = &rec
					changed = true
				}
			} else {
				merged := mergeWorkingStateRecord(*state.Goal, rec)
				state.Goal = &merged
				changed = true
			}
		}
	}
	changed = mergeWorkingStateSection(&state.HardConstraints, patch.HardConstraints, "hard_constraints", now, "") || changed
	changed = mergeWorkingStateSection(&state.Artifacts, patch.Artifacts, "artifacts", now, "") || changed
	changed = mergeWorkingStateSection(&state.Checks, patch.Checks, "checks", now, "") || changed
	changed = mergeWorkingStateSection(&state.OpenIssues, patch.OpenIssues, "open_issues", now, "") || changed
	changed = mergeWorkingStateSection(&state.LastSuccessfulChecks, patch.LastSuccessfulChecks, "last_successful_checks", now, "") || changed
	changed = mergeWorkingStateSection(&state.StaleChecks, patch.StaleChecks, "stale_checks", now, "") || changed
	if !changed {
		return nil
	}
	state.UpdatedAt = now
	return s.saveLocked(state)
}

func (s *WorkingStateStore) MarkPathsStale(paths []string, toolName, toolUseID string) error {
	if s == nil || len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	now := s.now()
	paths = normalizeWorkingStatePaths(paths)
	if len(paths) == 0 {
		return nil
	}
	var stale []WorkingStateRecord
	remaining := state.LastSuccessfulChecks[:0]
	for _, rec := range state.LastSuccessfulChecks {
		if rec.ResolvedAt.IsZero() && pathsIntersect(rec.RelatedPaths, paths) {
			staleRec := rec
			staleRec.ID = "stale-" + rec.ID
			staleRec.Text = fmt.Sprintf("check became stale after %s %s: %s", toolName, toolUseID, rec.Text)
			staleRec.Source = WorkingStateSourceToolResult
			staleRec.Severity = maxWorkingStateSeverity(rec.Severity, WorkingStateSeverityMedium)
			staleRec.CreatedAt = now
			staleRec.ResolvedAt = time.Time{}
			stale = append(stale, staleRec)
			continue
		}
		remaining = append(remaining, rec)
	}
	state.LastSuccessfulChecks = remaining
	if len(stale) == 0 {
		return nil
	}
	mergeWorkingStateSection(&state.StaleChecks, stale, "stale_checks", now, WorkingStateSourceToolResult)
	state.UpdatedAt = now
	return s.saveLocked(state)
}

func (s *WorkingStateStore) RecordSuccessfulCheck(obs WorkingStateCheckObservation) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	now := s.now()
	paths := normalizeWorkingStatePaths(obs.RelatedPaths)
	text := strings.TrimSpace(obs.Text)
	if text == "" {
		text = fmt.Sprintf("tool %s succeeded", obs.ToolName)
	}
	rec := normalizeWorkingStateRecord("checks", WorkingStateRecord{
		ID:           workingStateToolRecordID("check", obs.ToolName, obs.ToolUseID, paths),
		Text:         text,
		Source:       WorkingStateSourceToolResult,
		Confidence:   0.80,
		Severity:     WorkingStateSeverityLow,
		RelatedPaths: paths,
	}, now, WorkingStateSourceToolResult)
	mergeWorkingStateSection(&state.Checks, []WorkingStateRecord{rec}, "checks", now, WorkingStateSourceToolResult)
	mergeWorkingStateSection(&state.LastSuccessfulChecks, []WorkingStateRecord{rec}, "last_successful_checks", now, WorkingStateSourceToolResult)
	resolveWorkingStateRecords(state.StaleChecks, paths, now)
	resolveWorkingStateRecords(state.OpenIssues, paths, now)
	state.UpdatedAt = now
	return s.saveLocked(state)
}

func (s *WorkingStateStore) RecordOpenIssue(obs WorkingStateIssueObservation) error {
	if s == nil {
		return nil
	}
	severity := obs.Severity
	if severity == "" {
		severity = WorkingStateSeverityMedium
	}
	confidence := obs.Confidence
	if confidence <= 0 {
		confidence = 0.75
	}
	text := strings.TrimSpace(obs.Text)
	if text == "" {
		text = fmt.Sprintf("tool %s failed", obs.ToolName)
	}
	return s.ApplyPatch(WorkingStatePatch{OpenIssues: []WorkingStateRecord{{
		ID:           workingStateToolRecordID("issue", obs.ToolName, obs.ToolUseID, obs.RelatedPaths),
		Text:         text,
		Source:       WorkingStateSourceToolResult,
		Confidence:   confidence,
		Severity:     severity,
		RelatedPaths: obs.RelatedPaths,
	}}})
}

func (s *WorkingStateStore) RecordArtifactMutation(toolName, toolUseID string, paths []string) error {
	if s == nil || len(paths) == 0 {
		return nil
	}
	records := make([]WorkingStateRecord, 0, len(paths))
	for _, path := range normalizeWorkingStatePaths(paths) {
		records = append(records, WorkingStateRecord{
			ID:           workingStateToolRecordID("artifact", toolName, toolUseID, []string{path}),
			Text:         fmt.Sprintf("%s updated %s", toolName, path),
			Source:       WorkingStateSourceToolResult,
			Confidence:   0.90,
			Severity:     WorkingStateSeverityMedium,
			RelatedPaths: []string{path},
		})
	}
	if len(records) == 0 {
		return nil
	}
	return s.ApplyPatch(WorkingStatePatch{Artifacts: records})
}

func (s *WorkingStateStore) loadLocked() (WorkingState, error) {
	state := WorkingState{Version: 1}
	if s == nil || s.Path == "" {
		return state, nil
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("working state read: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("working state parse: %w", err)
	}
	if state.Version == 0 {
		state.Version = 1
	}
	return state, nil
}

func (s *WorkingStateStore) saveLocked(state WorkingState) error {
	if s == nil || s.Path == "" {
		return nil
	}
	state.Version = 1
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("working state mkdir: %w", err)
	}
	data, err := json.MarshalIndent(redactWorkingState(state), "", "  ")
	if err != nil {
		return fmt.Errorf("working state encode: %w", err)
	}
	data = append(data, '\n')
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("working state write: %w", err)
	}
	defer func() {
		_ = os.Remove(tmp)
	}()
	if err := os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("working state replace: %w", err)
	}
	return nil
}

func (s *WorkingStateStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (p WorkingStatePatch) empty() bool {
	return p.Goal == nil &&
		len(p.HardConstraints) == 0 &&
		len(p.Artifacts) == 0 &&
		len(p.Checks) == 0 &&
		len(p.OpenIssues) == 0 &&
		len(p.LastSuccessfulChecks) == 0 &&
		len(p.StaleChecks) == 0
}

func (s WorkingState) RenderProviderContext() (string, bool) {
	var b strings.Builder
	writeHeader := func(title string) {
		if b.Len() == 0 {
			b.WriteString("Runtime working state (advisory; do not override fresh verified evidence; low-confidence records are not blockers)\n")
		}
		b.WriteString(title)
		b.WriteString(":\n")
	}
	if s.Goal != nil && s.Goal.active() {
		writeHeader("Goal")
		writeWorkingStateRecord(&b, *s.Goal)
	}
	writeWorkingStateSection(&b, "Hard constraints", s.HardConstraints, writeHeader)
	writeWorkingStateSection(&b, "Open issues", s.OpenIssues, writeHeader)
	writeWorkingStateSection(&b, "Last successful checks", s.LastSuccessfulChecks, writeHeader)
	writeWorkingStateSection(&b, "Stale checks", s.StaleChecks, writeHeader)
	writeWorkingStateSection(&b, "Artifacts", s.Artifacts, writeHeader)
	if b.Len() == 0 {
		return "", false
	}
	return strings.TrimRight(b.String(), "\n"), true
}

func workingStateContextMessage(text string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, text)
	msg.ID = "runtime-working-state"
	return msg
}

func (e *Engine) workingStateStoreLocked() *WorkingStateStore {
	if e == nil || e.DisableWorkingState {
		return nil
	}
	if e.WorkingState != nil {
		return e.WorkingState
	}
	if e.Session == nil || e.Session.Dir == "" {
		return nil
	}
	e.WorkingState = NewWorkingStateStore(e.Session.Dir, WorkingStateOptions{})
	return e.WorkingState
}

func (e *Engine) WorkingStateStatusSnapshot() (*WorkingStateStatusSnapshot, error) {
	if e == nil {
		return nil, nil
	}
	e.mu.Lock()
	disabled := e.DisableWorkingState
	var store *WorkingStateStore
	if !disabled {
		store = e.workingStateStoreLocked()
	}
	e.mu.Unlock()

	if disabled {
		return &WorkingStateStatusSnapshot{
			Disabled: true,
			State:    WorkingState{Version: 1},
		}, nil
	}
	if store == nil {
		return nil, nil
	}
	state, err := store.Snapshot()
	if err != nil {
		return nil, err
	}
	present := false
	if store.Path != "" {
		if _, err := os.Stat(store.Path); err == nil {
			present = true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return &WorkingStateStatusSnapshot{
		Path:    store.Path,
		Present: present,
		State:   state,
	}, nil
}

func (e *Engine) workingStateContextLocked() (string, bool) {
	store := e.workingStateStoreLocked()
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (e *Engine) recordWorkingStateToolBatch(calls []llm.Block, results []llm.Block) error {
	store := e.workingStateStoreLocked()
	if store == nil {
		return nil
	}
	workDir := ""
	if e != nil && e.Session != nil {
		workDir = workDirFromSessionDir(e.Session.Dir)
	}
	for i, result := range results {
		var call llm.Block
		if i < len(calls) {
			call = calls[i]
		}
		toolName := firstNonEmptyString(result.ToolName, call.ToolName)
		toolUseID := firstNonEmptyString(result.ToolUseID, call.ToolUseID)
		paths := relatedPathsFromInput(workDir, call.Input)
		if result.IsError {
			errText := extractToolError(result.Content)
			if errText == "" {
				errText = strings.TrimSpace(result.Content)
			}
			classified := classifyToolFailure(toolFailureObservation{
				ToolName:  toolName,
				ToolUseID: toolUseID,
				Input:     call.Input,
				Content:   result.Content,
				Error:     errText,
				TimedOut:  strings.Contains(strings.ToLower(result.Content), "timed out"),
				ExitCode:  firstExitCode(nil, result.Content),
			})
			if err := store.RecordOpenIssue(WorkingStateIssueObservation{
				ToolName:     toolName,
				ToolUseID:    toolUseID,
				Text:         workingStateIssueText(toolName, toolUseID, errText, result.Content),
				Severity:     workingStateSeverityForFailure(classified.Classification),
				Confidence:   workingStateConfidenceForFailure(classified.Classification),
				RelatedPaths: paths,
			}); err != nil {
				return err
			}
			continue
		}
		if mutatesRelatedPath(toolName, paths, paths) {
			if err := store.RecordArtifactMutation(toolName, toolUseID, paths); err != nil {
				return err
			}
			if err := store.MarkPathsStale(paths, toolName, toolUseID); err != nil {
				return err
			}
		}
		if isWorkingStateCheckTool(toolName) {
			if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
				ToolName:     toolName,
				ToolUseID:    toolUseID,
				Text:         workingStateCheckText(toolName, toolUseID, result.Content),
				RelatedPaths: paths,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultWorkingStatePatchSource(patch *WorkingStatePatch, source WorkingStateSource) {
	if patch == nil {
		return
	}
	if patch.Goal != nil && patch.Goal.Source == "" {
		patch.Goal.Source = source
	}
	defaultRecordSource(patch.HardConstraints, source)
	defaultRecordSource(patch.Artifacts, source)
	defaultRecordSource(patch.Checks, source)
	defaultRecordSource(patch.OpenIssues, source)
	defaultRecordSource(patch.LastSuccessfulChecks, source)
	defaultRecordSource(patch.StaleChecks, source)
}

func defaultRecordSource(records []WorkingStateRecord, source WorkingStateSource) {
	for i := range records {
		if records[i].Source == "" {
			records[i].Source = source
		}
	}
}

func workingStateIssueText(toolName, toolUseID, errText, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool=%s", toolName)
	if toolUseID != "" {
		fmt.Fprintf(&b, " tool_use_id=%s", toolUseID)
	}
	b.WriteString(" failed")
	if strings.TrimSpace(errText) != "" {
		fmt.Fprintf(&b, ": %s", strings.TrimSpace(errText))
	} else if strings.TrimSpace(content) != "" {
		fmt.Fprintf(&b, ": %s", strings.TrimSpace(content))
	}
	errText = strings.TrimSpace(errText)
	if preview := strings.TrimSpace(content); preview != "" && preview != errText {
		fmt.Fprintf(&b, " output_preview=%q", preview)
	}
	return b.String()
}

func workingStateCheckText(toolName, toolUseID, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool=%s", toolName)
	if toolUseID != "" {
		fmt.Fprintf(&b, " tool_use_id=%s", toolUseID)
	}
	b.WriteString(" succeeded")
	if preview := strings.TrimSpace(content); preview != "" {
		fmt.Fprintf(&b, ": %s", preview)
	}
	return b.String()
}

func workingStateSeverityForFailure(class ToolFailureClassification) WorkingStateSeverity {
	switch class {
	case ToolFailureRuntimeFatal, ToolFailureRepeatedStuck:
		return WorkingStateSeverityCritical
	case ToolFailureExternalBlocked:
		return WorkingStateSeverityHigh
	case ToolFailureNonblockingExploratory:
		return WorkingStateSeverityLow
	default:
		return WorkingStateSeverityMedium
	}
}

func workingStateConfidenceForFailure(class ToolFailureClassification) float64 {
	switch class {
	case ToolFailureNonblockingExploratory:
		return 0.45
	case ToolFailureRuntimeFatal, ToolFailureRepeatedStuck:
		return 0.90
	default:
		return 0.75
	}
}

func isWorkingStateCheckTool(toolName string) bool {
	name := strings.ToLower(toolName)
	switch name {
	case "grep", "exec_command":
		return true
	}
	return containsAny(name, "check", "test", "lint", "build", "verify")
}

func writeWorkingStateSection(b *strings.Builder, title string, records []WorkingStateRecord, writeHeader func(string)) {
	active := activeWorkingStateRecords(records)
	if len(active) == 0 {
		return
	}
	writeHeader(title)
	for i, rec := range active {
		if i >= 8 {
			fmt.Fprintf(b, "- %d additional record(s) omitted\n", len(active)-i)
			break
		}
		writeWorkingStateRecord(b, rec)
	}
}

func writeWorkingStateRecord(b *strings.Builder, rec WorkingStateRecord) {
	fmt.Fprintf(b, "- severity=%s confidence=%.2f source=%s", rec.Severity, rec.Confidence, rec.Source)
	if len(rec.RelatedPaths) > 0 {
		fmt.Fprintf(b, " path=%s", strings.Join(rec.RelatedPaths, ","))
	}
	fmt.Fprintf(b, " :: %s\n", rec.Text)
}

func activeWorkingStateRecords(records []WorkingStateRecord) []WorkingStateRecord {
	out := make([]WorkingStateRecord, 0, len(records))
	for _, rec := range records {
		if rec.active() {
			out = append(out, rec)
		}
	}
	return out
}

func (r WorkingStateRecord) active() bool {
	return strings.TrimSpace(r.Text) != "" && r.ResolvedAt.IsZero()
}

func mergeWorkingStateSection(dst *[]WorkingStateRecord, incoming []WorkingStateRecord, section string, now time.Time, defaultSource WorkingStateSource) bool {
	if len(incoming) == 0 {
		return false
	}
	index := map[string]int{}
	for i, rec := range *dst {
		if rec.ID != "" {
			index[rec.ID] = i
		}
	}
	changed := false
	for _, rec := range incoming {
		rec = normalizeWorkingStateRecord(section, rec, now, defaultSource)
		if rec.ID == "" {
			continue
		}
		if i, ok := index[rec.ID]; ok {
			(*dst)[i] = mergeWorkingStateRecord((*dst)[i], rec)
		} else {
			if rec.Text == "" {
				continue
			}
			*dst = append(*dst, rec)
			index[rec.ID] = len(*dst) - 1
		}
		changed = true
	}
	return changed
}

func mergeWorkingStateRecord(base, incoming WorkingStateRecord) WorkingStateRecord {
	if incoming.Text != "" && (base.Text == "" || incoming.Confidence >= base.Confidence) {
		base.Text = incoming.Text
	}
	if incoming.Source != "" && (base.Source == "" || incoming.Confidence >= base.Confidence) {
		base.Source = incoming.Source
	}
	if incoming.Confidence > base.Confidence {
		base.Confidence = incoming.Confidence
	}
	base.Severity = maxWorkingStateSeverity(base.Severity, incoming.Severity)
	base.RelatedPaths = unionWorkingStatePaths(base.RelatedPaths, incoming.RelatedPaths)
	if base.CreatedAt.IsZero() || (!incoming.CreatedAt.IsZero() && incoming.CreatedAt.Before(base.CreatedAt)) {
		base.CreatedAt = incoming.CreatedAt
	}
	if !incoming.ResolvedAt.IsZero() {
		base.ResolvedAt = incoming.ResolvedAt
	} else if incoming.Text != "" {
		base.ResolvedAt = time.Time{}
	}
	return base
}

func normalizeWorkingStateRecord(section string, rec WorkingStateRecord, now time.Time, defaultSource WorkingStateSource) WorkingStateRecord {
	rec.Text = sanitizeWorkingStateText(strings.TrimSpace(rec.Text))
	if rec.Source == "" {
		rec.Source = defaultSource
	}
	if rec.Confidence < 0 {
		rec.Confidence = 0
	}
	if rec.Confidence > 1 {
		rec.Confidence = 1
	}
	if rec.Confidence == 0 && rec.Text != "" {
		rec.Confidence = 0.50
	}
	if rec.Severity == "" && rec.Text != "" {
		rec.Severity = WorkingStateSeverityMedium
	}
	rec.RelatedPaths = normalizeWorkingStatePaths(rec.RelatedPaths)
	if rec.CreatedAt.IsZero() && rec.Text != "" {
		rec.CreatedAt = now
	}
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = workingStateRecordID(section, rec)
	}
	return rec
}

func workingStateRecordID(section string, rec WorkingStateRecord) string {
	if section == "goal" {
		return "goal"
	}
	body := section + "\x00" + rec.Text + "\x00" + strings.Join(rec.RelatedPaths, "\x00")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return section + "-" + hex.EncodeToString(sum[:6])
}

func workingStateToolRecordID(kind, toolName, toolUseID string, paths []string) string {
	if strings.TrimSpace(toolUseID) != "" {
		return kind + "-" + safeArtifactName(toolUseID)
	}
	body := kind + "\x00" + toolName + "\x00" + strings.Join(normalizeWorkingStatePaths(paths), "\x00")
	sum := sha256.Sum256([]byte(body))
	return kind + "-" + hex.EncodeToString(sum[:6])
}

func resolveWorkingStateRecords(records []WorkingStateRecord, paths []string, now time.Time) {
	if len(paths) == 0 {
		return
	}
	for i := range records {
		if records[i].ResolvedAt.IsZero() && pathsIntersect(records[i].RelatedPaths, paths) {
			records[i].ResolvedAt = now
		}
	}
}

func normalizeWorkingStatePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.ReplaceAll(strings.TrimSpace(path), `\`, string(filepath.Separator))
		path = filepath.ToSlash(filepath.Clean(path))
		if path == "" || path == "." || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func unionWorkingStatePaths(a, b []string) []string {
	out := append([]string(nil), a...)
	out = append(out, b...)
	return normalizeWorkingStatePaths(out)
}

func maxWorkingStateSeverity(a, b WorkingStateSeverity) WorkingStateSeverity {
	if severityRank(b) > severityRank(a) {
		return b
	}
	if a == "" {
		return b
	}
	return a
}

func severityRank(s WorkingStateSeverity) int {
	switch s {
	case WorkingStateSeverityCritical:
		return 4
	case WorkingStateSeverityHigh:
		return 3
	case WorkingStateSeverityMedium:
		return 2
	case WorkingStateSeverityLow:
		return 1
	default:
		return 0
	}
}

func redactWorkingState(state WorkingState) WorkingState {
	if state.Goal != nil {
		rec := redactWorkingStateRecord(*state.Goal)
		state.Goal = &rec
	}
	state.HardConstraints = redactWorkingStateRecords(state.HardConstraints)
	state.Artifacts = redactWorkingStateRecords(state.Artifacts)
	state.Checks = redactWorkingStateRecords(state.Checks)
	state.OpenIssues = redactWorkingStateRecords(state.OpenIssues)
	state.LastSuccessfulChecks = redactWorkingStateRecords(state.LastSuccessfulChecks)
	state.StaleChecks = redactWorkingStateRecords(state.StaleChecks)
	return state
}

func redactWorkingStateRecords(records []WorkingStateRecord) []WorkingStateRecord {
	if len(records) == 0 {
		return records
	}
	out := append([]WorkingStateRecord(nil), records...)
	for i := range out {
		out[i] = redactWorkingStateRecord(out[i])
	}
	return out
}

func redactWorkingStateRecord(rec WorkingStateRecord) WorkingStateRecord {
	rec.Text = sanitizeWorkingStateText(rec.Text)
	return rec
}

var (
	workingStateSecretAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*[^ \n\r\t]+`)
	workingStateBearerPattern           = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]+`)
	workingStateOpenAIKeyPattern        = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)
)

func sanitizeWorkingStateText(text string) string {
	text = truncate(text, 1000)
	text = workingStateSecretAssignmentPattern.ReplaceAllString(text, "[REDACTED]")
	text = workingStateBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = workingStateOpenAIKeyPattern.ReplaceAllString(text, "[REDACTED]")
	return text
}
