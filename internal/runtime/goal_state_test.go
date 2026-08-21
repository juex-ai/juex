package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func TestGoalStateStoreCreatesAndUpdatesModelOwnedGoal(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	store := workmem.NewGoalStateStore(t.TempDir(), workmem.GoalStateOptions{Now: func() time.Time { return now }})

	state, err := store.CreateWithContract(workmem.GoalStateCreate{
		Description:  "ship feature with api_key=secret",
		Acceptance:   "tests pass; dist/report.json exists and is valid JSON",
		StatusReason: "waiting for validation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != workmem.GoalStatusInProgress || state.Description == "" || !strings.Contains(state.Acceptance, "dist/report.json") {
		t.Fatalf("created state = %+v", state)
	}
	if strings.Contains(state.Description, "secret") {
		t.Fatalf("description not redacted: %q", state.Description)
	}

	reason := "all validation passed"
	acceptance := "go test ./internal/runtime passes"
	state, err = store.Update(workmem.GoalStateUpdate{
		Acceptance:   &acceptance,
		Status:       workmem.GoalStatusSuccess,
		StatusReason: &reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != workmem.GoalStatusSuccess || state.StatusReason != reason || state.Acceptance != acceptance {
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

func TestGoalStateStorePreservesAndRedactsLongAcceptance(t *testing.T) {
	store := workmem.NewGoalStateStore(t.TempDir(), workmem.GoalStateOptions{})
	acceptance := strings.Repeat("required-check ", 100) + "api_key=secret final-check-must-survive"

	state, err := store.CreateWithContract(workmem.GoalStateCreate{
		Description: "ship the complete contract",
		Acceptance:  acceptance,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Acceptance) <= 1000 || !strings.Contains(state.Acceptance, "final-check-must-survive") {
		t.Fatalf("long acceptance was truncated: len=%d tail=%q", len(state.Acceptance), state.Acceptance[max(0, len(state.Acceptance)-80):])
	}
	if strings.Contains(state.Acceptance, "secret") {
		t.Fatalf("long acceptance retained a secret: %q", state.Acceptance)
	}
	rendered, ok := state.RenderProviderContext()
	if !ok || !strings.Contains(rendered, "final-check-must-survive") || strings.Contains(rendered, "secret") {
		t.Fatalf("provider context did not preserve the redacted contract:\n%s", rendered)
	}
}

func TestGoalStateGateContinuesOnlyForInProgressGoal(t *testing.T) {
	store := workmem.NewGoalStateStore(t.TempDir(), workmem.GoalStateOptions{})
	decision, err := store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlockStop {
		t.Fatalf("no goal should not block: %+v", decision)
	}

	if _, err := store.CreateWithContract(workmem.GoalStateCreate{
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
		!strings.Contains(decision.ContinuePrompt, "wait_for_user") ||
		!strings.Contains(decision.ContinuePrompt, "artifact.txt") ||
		!strings.Contains(decision.ContinuePrompt, "go test ./... passes") {
		t.Fatalf("in-progress decision = %+v", decision)
	}
	recorded, err := store.RecordContinuation(decision)
	if err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("in-progress continuation was not recorded")
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.ContinuationCount != 1 {
		t.Fatalf("continuation_count = %d", state.ContinuationCount)
	}

	waitReason := "waiting for the user to approve the release"
	if _, err := store.Update(workmem.GoalStateUpdate{Status: workmem.GoalStatusWaitForUser, StatusReason: &waitReason}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if decision.BlockStop || decision.Status != workmem.GoalStatusWaitForUser {
		t.Fatalf("wait-for-user should allow finish: %+v", decision)
	}
	recorded, err = store.RecordContinuation(decision)
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("wait-for-user continuation was recorded")
	}
	state, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.ContinuationCount != 1 {
		t.Fatalf("wait-for-user changed continuation_count = %d", state.ContinuationCount)
	}

	if _, err := store.Update(workmem.GoalStateUpdate{Status: workmem.GoalStatusFailure}); err != nil {
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

func TestGoalStateStoreRejectsStaleContinuationAfterTerminalUpdate(t *testing.T) {
	store := workmem.NewGoalStateStore(t.TempDir(), workmem.GoalStateOptions{})
	if _, err := store.Create("finish delegated work", "all worker results arrive"); err != nil {
		t.Fatal(err)
	}
	decision, err := store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if !decision.BlockStop {
		t.Fatalf("in-progress decision = %+v", decision)
	}
	if _, err := store.Update(workmem.GoalStateUpdate{Status: workmem.GoalStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordContinuation(decision)
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("stale continuation was recorded after Goal success")
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != workmem.GoalStatusSuccess || state.ContinuationCount != 0 {
		t.Fatalf("goal state = %+v", state)
	}
}

func TestGoalStateProviderContextRendersCompactContract(t *testing.T) {
	state := workmem.GoalState{
		Description:  "complete\ndocs",
		Acceptance:   "docs/guide.md is reviewed\nby tester, published, and passes link checks",
		Status:       workmem.GoalStatusInProgress,
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
	state := workmem.GoalState{
		Description: "complete task",
		Acceptance:  "tests pass",
		Status:      workmem.GoalStatusInProgress,
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
	state := workmem.GoalState{StatusReason: "explanatory text without a goal"}
	if snapshot := state.StatusSnapshot(); snapshot != nil {
		t.Fatalf("status_reason alone should not create a goal: %+v", snapshot)
	}
	if rendered, ok := state.RenderProviderContext(); ok || rendered != "" {
		t.Fatalf("status_reason alone should not render provider context: %q", rendered)
	}
}
