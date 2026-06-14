package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/tools"
)

func TestWorkingStateStoreMergeConfidenceSeverityAndResolvedAt(t *testing.T) {
	now := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	store := NewWorkingStateStore(t.TempDir(), WorkingStateOptions{Now: func() time.Time { return now }})

	if err := store.ApplyPatch(WorkingStatePatch{HardConstraints: []WorkingStateRecord{{
		ID:           "hc-validate",
		Text:         "run validation before final",
		Source:       WorkingStateSourceUserInput,
		Confidence:   0.40,
		Severity:     WorkingStateSeverityMedium,
		RelatedPaths: []string{"a.go"},
	}}}); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Minute)
	store.Now = func() time.Time { return later }
	if err := store.ApplyPatch(WorkingStatePatch{HardConstraints: []WorkingStateRecord{{
		ID:           "hc-validate",
		Text:         "run validation before final",
		Source:       WorkingStateSourceHookExtraction,
		Confidence:   0.95,
		Severity:     WorkingStateSeverityCritical,
		RelatedPaths: []string{"b.go"},
	}}}); err != nil {
		t.Fatal(err)
	}

	resolvedAt := later.Add(time.Minute)
	if err := store.ApplyPatch(WorkingStatePatch{HardConstraints: []WorkingStateRecord{{
		ID:         "hc-validate",
		ResolvedAt: resolvedAt,
	}}}); err != nil {
		t.Fatal(err)
	}

	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.HardConstraints) != 1 {
		t.Fatalf("hard constraints = %+v", state.HardConstraints)
	}
	rec := state.HardConstraints[0]
	if rec.Confidence != 0.95 || rec.Severity != WorkingStateSeverityCritical || rec.Source != WorkingStateSourceHookExtraction {
		t.Fatalf("merged record lost strongest facts: %+v", rec)
	}
	if !rec.CreatedAt.Equal(now) || !rec.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("timestamps = created %s resolved %s", rec.CreatedAt, rec.ResolvedAt)
	}
	if strings.Join(rec.RelatedPaths, ",") != "a.go,b.go" {
		t.Fatalf("related paths = %+v", rec.RelatedPaths)
	}
	rendered, ok := state.RenderProviderContext()
	if ok || strings.Contains(rendered, "run validation before final") {
		t.Fatalf("resolved record should be omitted from provider context:\n%s", rendered)
	}
}

func TestWorkingStateStoreStalesAndRefreshesRelatedChecks(t *testing.T) {
	now := time.Date(2026, 6, 14, 11, 0, 0, 0, time.UTC)
	store := NewWorkingStateStore(t.TempDir(), WorkingStateOptions{Now: func() time.Time { return now }})
	target := filepath.Join(t.TempDir(), "artifact.txt")

	if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
		ToolName:     "check_ready",
		ToolUseID:    "check-1",
		Text:         "artifact ready",
		RelatedPaths: []string{target},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPathsStale([]string{target}, "write", "write-1"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LastSuccessfulChecks) != 0 || len(activeWorkingStateRecords(state.StaleChecks)) != 1 {
		t.Fatalf("after stale last=%+v stale=%+v", state.LastSuccessfulChecks, state.StaleChecks)
	}

	store.Now = func() time.Time { return now.Add(time.Minute) }
	if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
		ToolName:     "check_ready",
		ToolUseID:    "check-2",
		Text:         "artifact ready after rewrite",
		RelatedPaths: []string{target},
	}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LastSuccessfulChecks) != 1 {
		t.Fatalf("last successful checks = %+v", state.LastSuccessfulChecks)
	}
	if len(activeWorkingStateRecords(state.StaleChecks)) != 0 {
		t.Fatalf("stale checks should be resolved: %+v", state.StaleChecks)
	}
}

func TestWorkingStateActiveContextInjectionEmptyAndDisabled(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.WorkingState = NewWorkingStateStore(eng.Session.Dir, WorkingStateOptions{})

	if got := messagesText(eng.ActiveContext().Messages); strings.Contains(got, "Runtime working state") {
		t.Fatalf("empty sidecar should not be injected:\n%s", got)
	}
	if err := eng.WorkingState.ApplyPatch(WorkingStatePatch{HardConstraints: []WorkingStateRecord{{
		Text:       "keep the API stable",
		Source:     WorkingStateSourceUserInput,
		Confidence: 0.90,
		Severity:   WorkingStateSeverityHigh,
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := messagesText(eng.ActiveContext().Messages); !strings.Contains(got, "Runtime working state") || !strings.Contains(got, "keep the API stable") {
		t.Fatalf("sidecar not injected:\n%s", got)
	}

	eng.DisableWorkingState = true
	if got := messagesText(eng.ActiveContext().Messages); strings.Contains(got, "Runtime working state") {
		t.Fatalf("disabled sidecar should not be injected:\n%s", got)
	}
}

func TestWorkingStateHookOutputMergesPatch(t *testing.T) {
	runner := &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventUserPromptSubmit: {{
			WorkingState: mustRawMessage(t, `{"hard_constraints":[{"text":"do not skip tests","confidence":0.88,"severity":"high"}]}`),
		}},
	}}
	prov := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}}}
	eng, _ := newEngine(t, prov, false)
	eng.Hooks = runner

	if _, err := eng.Turn(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	state, err := eng.WorkingState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.HardConstraints) != 1 || state.HardConstraints[0].Source != WorkingStateSourceHookExtraction {
		t.Fatalf("hook working state = %+v", state.HardConstraints)
	}
}

func TestWorkingStateToolResultsUpdateSidecarAndRedactSecrets(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "artifact.txt")
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check-1", ToolName: "check_ready", Input: map[string]any{"path": target}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "write-1", ToolName: "write", Input: map[string]any{"path": target, "content": "ready\n"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check-2", ToolName: "check_ready", Input: map[string]any{"path": target}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, true)
	eng.WorkingState = NewWorkingStateStore(eng.Session.Dir, WorkingStateOptions{})
	eng.Tools.MustRegister(tools.Tool{
		Name: "check_ready",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			data, err := os.ReadFile(target)
			if err != nil || !strings.Contains(string(data), "ready") {
				return "SECRET_TOKEN=super-secret-token artifact missing", os.ErrNotExist
			}
			return "artifact ready", nil
		},
	})

	if out, err := eng.Turn(context.Background(), "make ready"); err != nil || out != "done" {
		t.Fatalf("turn out=%q err=%v", out, err)
	}
	state, err := eng.WorkingState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Artifacts) == 0 || len(state.LastSuccessfulChecks) == 0 {
		t.Fatalf("state missing artifact/check: %+v", state)
	}
	if len(activeWorkingStateRecords(state.OpenIssues)) != 0 {
		t.Fatalf("open issue should be resolved: %+v", state.OpenIssues)
	}
	data, err := os.ReadFile(filepath.Join(eng.Session.Dir, "working_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret-token") || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("working state file did not redact secret:\n%s", string(data))
	}
}

func mustRawMessage(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
