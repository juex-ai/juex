package workmem

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
)

const workingStateFile = "working_state.json"

const (
	workingStateHardConstraintsActiveLimit        = 16
	workingStateHardConstraintsResolvedLimit      = 4
	workingStateArtifactsActiveLimit              = 16
	workingStateArtifactsResolvedLimit            = 4
	workingStateChecksActiveLimit                 = 24
	workingStateChecksResolvedLimit               = 0
	workingStateOpenIssuesActiveLimit             = 16
	workingStateOpenIssuesResolvedLimit           = 4
	workingStateToolFailuresActiveLimit           = 16
	workingStateToolFailuresResolvedLimit         = 4
	workingStateLastSuccessfulChecksActiveLimit   = 12
	workingStateLastSuccessfulChecksResolvedLimit = 0
	workingStateStaleChecksActiveLimit            = 12
	workingStateStaleChecksResolvedLimit          = 4
	workingStateActiveProcessesActiveLimit        = 16
	workingStateActiveProcessesResolvedLimit      = 4
	workingStateRuntimeBudgetActiveLimit          = 4
	workingStateRuntimeBudgetResolvedLimit        = 0
)

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
	ToolFailures         []WorkingStateRecord `json:"tool_failures,omitempty"`
	LastSuccessfulChecks []WorkingStateRecord `json:"last_successful_checks,omitempty"`
	StaleChecks          []WorkingStateRecord `json:"stale_checks,omitempty"`
	ActiveProcesses      []WorkingStateRecord `json:"active_processes,omitempty"`
	RuntimeBudget        []WorkingStateRecord `json:"runtime_budget,omitempty"`
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
	ToolFailures         []WorkingStateRecord `json:"tool_failures,omitempty"`
	LastSuccessfulChecks []WorkingStateRecord `json:"last_successful_checks,omitempty"`
	StaleChecks          []WorkingStateRecord `json:"stale_checks,omitempty"`
	ActiveProcesses      []WorkingStateRecord `json:"active_processes,omitempty"`
	RuntimeBudget        []WorkingStateRecord `json:"runtime_budget,omitempty"`
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

func (s *WorkingStateStore) StatusSnapshot() (*WorkingStateStatusSnapshot, error) {
	if s == nil {
		return nil, nil
	}
	state, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	present := false
	if s.Path != "" {
		if _, err := os.Stat(s.Path); err == nil {
			present = true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return &WorkingStateStatusSnapshot{
		Path:    s.Path,
		Present: present,
		State:   state,
	}, nil
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
	changed = mergeWorkingStateSection(&state.ToolFailures, patch.ToolFailures, "tool_failures", now, "") || changed
	changed = mergeWorkingStateSection(&state.LastSuccessfulChecks, patch.LastSuccessfulChecks, "last_successful_checks", now, "") || changed
	changed = mergeWorkingStateSection(&state.StaleChecks, patch.StaleChecks, "stale_checks", now, "") || changed
	changed = mergeWorkingStateSection(&state.ActiveProcesses, patch.ActiveProcesses, "active_processes", now, "") || changed
	changed = mergeWorkingStateSection(&state.RuntimeBudget, patch.RuntimeBudget, "runtime_budget", now, "") || changed
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
	resolveWorkingStateRecords(state.ToolFailures, paths, now)
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
	rec := WorkingStateRecord{
		ID:           workingStateToolRecordID("issue", obs.ToolName, obs.ToolUseID, obs.RelatedPaths),
		Text:         text,
		Source:       WorkingStateSourceToolResult,
		Confidence:   confidence,
		Severity:     severity,
		RelatedPaths: obs.RelatedPaths,
	}
	failure := rec
	failure.ID = workingStateToolRecordID("tool_failure", obs.ToolName, obs.ToolUseID, obs.RelatedPaths)
	return s.ApplyPatch(WorkingStatePatch{
		OpenIssues:   []WorkingStateRecord{rec},
		ToolFailures: []WorkingStateRecord{failure},
	})
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
	return pruneWorkingState(state), nil
}

func (s *WorkingStateStore) saveLocked(state WorkingState) error {
	if s == nil || s.Path == "" {
		return nil
	}
	state.Version = 1
	state = pruneWorkingState(state)
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
		len(p.ToolFailures) == 0 &&
		len(p.LastSuccessfulChecks) == 0 &&
		len(p.StaleChecks) == 0 &&
		len(p.ActiveProcesses) == 0 &&
		len(p.RuntimeBudget) == 0
}

func (s WorkingState) RenderProviderContext() (string, bool) {
	var b strings.Builder
	writeHeader := func(title string) {
		if b.Len() == 0 {
			b.WriteString("Current working observations (runtime-observed; advisory; do not redefine the goal contract; low-confidence records are not blockers)\n")
		}
		b.WriteString(title)
		b.WriteString(":\n")
	}
	if s.Goal != nil && s.Goal.active() {
		writeHeader("Observed summary")
		writeWorkingStateRecord(&b, *s.Goal)
	}
	writeWorkingStateSection(&b, "Hard constraints", s.HardConstraints, writeHeader)
	writeWorkingStateSection(&b, "Open issues", s.OpenIssues, writeHeader)
	writeWorkingStateSection(&b, "Tool failures", s.ToolFailures, writeHeader)
	writeWorkingStateSection(&b, "Active processes", s.ActiveProcesses, writeHeader)
	writeWorkingStateSection(&b, "Runtime budget", s.RuntimeBudget, writeHeader)
	writeWorkingStateSection(&b, "Last successful checks", s.LastSuccessfulChecks, writeHeader)
	writeWorkingStateSection(&b, "Stale checks", s.StaleChecks, writeHeader)
	writeWorkingStateSection(&b, "Artifacts", s.Artifacts, writeHeader)
	if b.Len() == 0 {
		return "", false
	}
	return strings.TrimRight(b.String(), "\n"), true
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

func pruneWorkingState(state WorkingState) WorkingState {
	state.HardConstraints = pruneWorkingStateRecords(state.HardConstraints, workingStateHardConstraintsActiveLimit, workingStateHardConstraintsResolvedLimit)
	state.Artifacts = pruneWorkingStateRecords(state.Artifacts, workingStateArtifactsActiveLimit, workingStateArtifactsResolvedLimit)
	state.Checks = pruneWorkingStateRecords(state.Checks, workingStateChecksActiveLimit, workingStateChecksResolvedLimit)
	state.OpenIssues = pruneWorkingStateRecords(state.OpenIssues, workingStateOpenIssuesActiveLimit, workingStateOpenIssuesResolvedLimit)
	state.ToolFailures = pruneWorkingStateRecords(state.ToolFailures, workingStateToolFailuresActiveLimit, workingStateToolFailuresResolvedLimit)
	state.LastSuccessfulChecks = pruneWorkingStateRecords(state.LastSuccessfulChecks, workingStateLastSuccessfulChecksActiveLimit, workingStateLastSuccessfulChecksResolvedLimit)
	state.StaleChecks = pruneWorkingStateRecords(state.StaleChecks, workingStateStaleChecksActiveLimit, workingStateStaleChecksResolvedLimit)
	state.ActiveProcesses = pruneWorkingStateRecords(state.ActiveProcesses, workingStateActiveProcessesActiveLimit, workingStateActiveProcessesResolvedLimit)
	state.RuntimeBudget = pruneWorkingStateRecords(state.RuntimeBudget, workingStateRuntimeBudgetActiveLimit, workingStateRuntimeBudgetResolvedLimit)
	return state
}

func pruneWorkingStateRecords(records []WorkingStateRecord, activeLimit, resolvedLimit int) []WorkingStateRecord {
	if len(records) == 0 {
		return records
	}
	active := make([]WorkingStateRecord, 0, len(records))
	resolved := make([]WorkingStateRecord, 0, len(records))
	for _, rec := range records {
		if rec.active() {
			active = append(active, rec)
			continue
		}
		if strings.TrimSpace(rec.Text) != "" {
			resolved = append(resolved, rec)
		}
	}
	sortWorkingStateActiveRecords(active)
	sortWorkingStateResolvedRecords(resolved)
	active = limitWorkingStateRecords(active, activeLimit)
	resolved = limitWorkingStateRecords(resolved, resolvedLimit)
	out := make([]WorkingStateRecord, 0, len(active)+len(resolved))
	out = append(out, active...)
	out = append(out, resolved...)
	return out
}

func limitWorkingStateRecords(records []WorkingStateRecord, limit int) []WorkingStateRecord {
	if limit < 0 {
		limit = 0
	}
	if len(records) <= limit {
		return records
	}
	return records[:limit]
}

func sortWorkingStateActiveRecords(records []WorkingStateRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		left, right := records[i], records[j]
		if leftRank, rightRank := severityRank(left.Severity), severityRank(right.Severity); leftRank != rightRank {
			return leftRank > rightRank
		}
		return workingStateRecordNewer(left, right, false)
	})
}

func sortWorkingStateResolvedRecords(records []WorkingStateRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		return workingStateRecordNewer(records[i], records[j], true)
	})
}

func workingStateRecordNewer(left, right WorkingStateRecord, preferResolved bool) bool {
	leftTime := workingStateRecordSortTime(left, preferResolved)
	rightTime := workingStateRecordSortTime(right, preferResolved)
	if !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	return left.ID < right.ID
}

func workingStateRecordSortTime(rec WorkingStateRecord, preferResolved bool) time.Time {
	if preferResolved && !rec.ResolvedAt.IsZero() {
		return rec.ResolvedAt
	}
	if !rec.CreatedAt.IsZero() {
		return rec.CreatedAt
	}
	return rec.ResolvedAt
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

func safeArtifactName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
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

func pathsIntersect(a []string, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, path := range a {
		seen[filepath.Clean(path)] = true
	}
	for _, path := range b {
		if seen[filepath.Clean(path)] {
			return true
		}
	}
	return false
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
	state.ToolFailures = redactWorkingStateRecords(state.ToolFailures)
	state.LastSuccessfulChecks = redactWorkingStateRecords(state.LastSuccessfulChecks)
	state.StaleChecks = redactWorkingStateRecords(state.StaleChecks)
	state.ActiveProcesses = redactWorkingStateRecords(state.ActiveProcesses)
	state.RuntimeBudget = redactWorkingStateRecords(state.RuntimeBudget)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s...(truncated, total %d bytes)", s[:n], len(s))
}
