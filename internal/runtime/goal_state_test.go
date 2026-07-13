package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoalStateStoreCreatesAndUpdatesModelOwnedGoal(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{Now: func() time.Time { return now }})

	state, err := store.CreateWithContract(GoalStateCreate{
		Description:  "ship feature with api_key=secret",
		Acceptance:   "tests pass; dist/report.json exists and is valid JSON",
		StatusReason: "waiting for validation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusInProgress || state.Description == "" || !strings.Contains(state.Acceptance, "dist/report.json") {
		t.Fatalf("created state = %+v", state)
	}
	if strings.Contains(state.Description, "secret") {
		t.Fatalf("description not redacted: %q", state.Description)
	}

	reason := "all validation passed"
	acceptance := "go test ./internal/runtime passes"
	state, err = store.Update(GoalStateUpdate{
		Acceptance:   &acceptance,
		Status:       GoalStatusSuccess,
		StatusReason: &reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusSuccess || state.StatusReason != reason || state.Acceptance != acceptance {
		t.Fatalf("updated state = %+v", state)
	}

	data, err := os.ReadFile(filepath.Join(store.SessionDir, "goal_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"acceptance_criteria", "required_artifacts", "artifact_requirements", "validation_requirements", "verification_method", "objective", "evidence", "budget", "blocked_reason", "next_user_input", "last_progress", "last_check", "secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("goal_state.json contains old or unredacted field %q:\n%s", forbidden, text)
		}
	}
	for _, want := range []string{`"acceptance"`, `"status_reason"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("goal_state.json missing %s:\n%s", want, text)
		}
	}
}

func TestGoalStateGateContinuesOnlyForInProgressGoal(t *testing.T) {
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{})
	decision, err := store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlockStop {
		t.Fatalf("no goal should not block: %+v", decision)
	}

	if _, err := store.CreateWithContract(GoalStateCreate{
		Description: "finish task",
		Acceptance:  "artifact.txt exists and go test ./... passes",
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if !decision.BlockStop || decision.Reason != "goal_in_progress" ||
		!strings.Contains(decision.ContinuePrompt, "Current goal contract") ||
		!strings.Contains(decision.ContinuePrompt, "artifact.txt") ||
		!strings.Contains(decision.ContinuePrompt, "go test ./... passes") {
		t.Fatalf("in-progress decision = %+v", decision)
	}
	if err := store.RecordContinuation(decision); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.ContinuationCount != 1 {
		t.Fatalf("continuation_count = %d", state.ContinuationCount)
	}

	if _, err := store.Update(GoalStateUpdate{Status: GoalStatusFailure}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlockStop {
		t.Fatalf("terminal failure should allow finish: %+v", decision)
	}
}

func TestGoalStateStoreIgnoresRemovedLegacyFieldsWithoutMigrating(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "goal_state.json"), []byte(`{
  "version": 1,
  "description": "current description survives",
  "acceptance_criteria": ["legacy criterion"],
  "required_artifacts": ["legacy.txt"],
  "verification_method": "legacy verification",
  "status": "success",
  "budget": {"continuations_used": 2},
  "last_progress": "old"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := NewGoalStateStore(dir, GoalStateOptions{}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Description != "current description survives" || state.Acceptance != "" || state.Status != GoalStatusSuccess || state.ContinuationCount != 0 {
		t.Fatalf("legacy state = %+v", state)
	}
}

func TestGoalStateStoreDoesNotRecoverLegacyOnlyGoal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "goal_state.json"), []byte(`{
  "version": 1,
  "objective": "legacy objective",
  "acceptance_criteria": ["legacy criterion"],
  "status": "complete",
  "budget": {"continuations_used": 2}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewGoalStateStore(dir, GoalStateOptions{}).StatusSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("legacy-only goal should be absent: %+v", snapshot)
	}
}

func TestGoalStateProviderContextRendersCompactContract(t *testing.T) {
	state := GoalState{
		Description:  "complete\ndocs",
		Acceptance:   "docs/guide.md is reviewed\nby tester, published, and passes link checks",
		Status:       GoalStatusInProgress,
		StatusReason: "docs still need review",
	}
	rendered, ok := state.RenderProviderContext()
	if !ok {
		t.Fatal("expected provider context")
	}
	for _, want := range []string{
		"Current goal contract",
		"acceptance",
		"status reason",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("provider context missing %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(rendered, "complete docs") || !strings.Contains(rendered, "reviewed by tester") {
		t.Fatalf("provider context should collapse multiline values:\n%s", rendered)
	}
	if lines := strings.Count(rendered, "\n") + 1; lines != 5 {
		t.Fatalf("provider context lines = %d, want 5:\n%s", lines, rendered)
	}
}

func TestGoalStateProviderContextOmitsEmptyStatusReason(t *testing.T) {
	state := GoalState{
		Description: "complete task",
		Acceptance:  "tests pass",
		Status:      GoalStatusInProgress,
	}
	rendered, ok := state.RenderProviderContext()
	if !ok {
		t.Fatal("expected provider context")
	}
	if strings.Contains(rendered, "status reason") {
		t.Fatalf("provider context should omit an empty reason:\n%s", rendered)
	}
	if lines := strings.Count(rendered, "\n") + 1; lines != 4 {
		t.Fatalf("provider context lines = %d, want 4:\n%s", lines, rendered)
	}
}

func TestGoalStateStatusReasonAloneDoesNotCreateGoal(t *testing.T) {
	state := GoalState{StatusReason: "explanatory text without a goal"}
	if snapshot := state.StatusSnapshot(); snapshot != nil {
		t.Fatalf("status_reason alone should not create a goal: %+v", snapshot)
	}
	if rendered, ok := state.RenderProviderContext(); ok || rendered != "" {
		t.Fatalf("status_reason alone should not render provider context: %q", rendered)
	}
}
