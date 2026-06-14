package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoalStateStoreBeginsTurnAndAppliesCompletionPatch(t *testing.T) {
	now := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{Now: func() time.Time { return now }})

	if err := store.BeginTurn("ship feature with api_key=secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPatch(GoalStatePatch{
		Status:       GoalStatusContinue,
		LastProgress: "implementation exists",
		Evidence: []GoalEvidence{{
			ID:     "tests-missing",
			Kind:   "missing_check",
			Text:   "token=abc123 tests have not run",
			Source: "hook",
		}},
		CompletionCheck: &CompletionCheck{
			Status:         GoalStatusContinue,
			Summary:        "tests are still missing",
			ContinuePrompt: "Run the focused tests before finishing.",
			Source:         "hook",
		},
	}); err != nil {
		t.Fatal(err)
	}

	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusContinue {
		t.Fatalf("status = %q", state.Status)
	}
	if state.Objective == "" || strings.Contains(state.Objective, "secret") {
		t.Fatalf("objective not stored/redacted: %q", state.Objective)
	}
	if state.Budget.MaxContinuations != DefaultGoalMaxContinuations {
		t.Fatalf("max continuations = %d", state.Budget.MaxContinuations)
	}
	if state.LastCheck == nil || state.LastCheck.ContinuePrompt == "" {
		t.Fatalf("last check = %+v", state.LastCheck)
	}
	if len(state.Evidence) != 1 || strings.Contains(state.Evidence[0].Text, "abc123") {
		t.Fatalf("evidence not stored/redacted: %+v", state.Evidence)
	}
	data, err := os.ReadFile(filepath.Join(store.SessionDir, "goal_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "abc123") {
		t.Fatalf("goal_state.json leaked secret-like text:\n%s", data)
	}

	if err := store.ApplyPatch(GoalStatePatch{CompletionCheck: &CompletionCheck{
		Status:  GoalStatusComplete,
		Summary: "checked complete",
	}}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusComplete {
		t.Fatalf("status from completion_check = %q", state.Status)
	}
}

func TestGoalStateGateDecisions(t *testing.T) {
	now := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{Now: func() time.Time { return now }})
	if err := store.BeginTurn("finish task"); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPatch(GoalStatePatch{
		Status: GoalStatusContinue,
		CompletionCheck: &CompletionCheck{
			Status:         GoalStatusContinue,
			Summary:        "verification missing",
			ContinuePrompt: "Run verification.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	decision, err := store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if !decision.BlockStop || decision.ContinuePrompt != "Run verification." {
		t.Fatalf("continue decision = %+v", decision)
	}
	if err := store.RecordContinuation(decision); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Budget.ContinuationsUsed != 1 {
		t.Fatalf("continuations used = %d", state.Budget.ContinuationsUsed)
	}

	if err := store.ApplyPatch(GoalStatePatch{
		Status:        GoalStatusBlocked,
		BlockedReason: "CI provider is unavailable",
		NextUserInput: "Retry after CI recovers.",
		CompletionCheck: &CompletionCheck{
			Status:  GoalStatusBlocked,
			Summary: "external CI unavailable",
		},
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlockStop {
		t.Fatalf("blocked with reason should allow finish: %+v", decision)
	}

	if err := store.ApplyPatch(GoalStatePatch{
		Status:        GoalStatusBlocked,
		BlockedReason: "",
		NextUserInput: "",
		CompletionCheck: &CompletionCheck{
			Status:  GoalStatusBlocked,
			Summary: "blocked but incomplete",
		},
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if !decision.BlockStop || !strings.Contains(decision.ContinuePrompt, "blocked_reason") {
		t.Fatalf("blocked without reason decision = %+v", decision)
	}

	if err := store.ApplyPatch(GoalStatePatch{
		Status: GoalStatusContinue,
		Budget: &GoalBudget{MaxContinuations: 1, ContinuationsUsed: 1},
		CompletionCheck: &CompletionCheck{
			Status:         GoalStatusContinue,
			Summary:        "still missing",
			ContinuePrompt: "try again",
		},
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlockStop || decision.Reason != "continuation_budget_exhausted" {
		t.Fatalf("budget exhausted decision = %+v", decision)
	}
}
