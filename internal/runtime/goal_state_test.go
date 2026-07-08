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
		Description:            "ship feature with api_key=secret",
		AcceptanceCriteria:     []string{"tests pass", "artifact exists"},
		RequiredArtifacts:      []string{"dist/report.json"},
		ArtifactRequirements:   []string{"report is valid JSON"},
		ValidationRequirements: []string{"go test ./..."},
		VerificationMethod:     "run focused tests",
		StatusReason:           "waiting for validation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusInProgress || state.Description == "" || len(state.AcceptanceCriteria) != 2 || len(state.RequiredArtifacts) != 1 {
		t.Fatalf("created state = %+v", state)
	}
	if strings.Contains(state.Description, "secret") {
		t.Fatalf("description not redacted: %q", state.Description)
	}

	reason := "all validation passed"
	validation := []string{"go test ./internal/runtime"}
	state, err = store.Update(GoalStateUpdate{
		Status:                 GoalStatusSuccess,
		StatusReason:           &reason,
		ValidationRequirements: &validation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusSuccess || state.StatusReason != reason || strings.Join(state.ValidationRequirements, ",") != validation[0] {
		t.Fatalf("updated state = %+v", state)
	}

	data, err := os.ReadFile(filepath.Join(store.SessionDir, "goal_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"objective", "evidence", "budget", "blocked_reason", "next_user_input", "last_progress", "last_check", "secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("goal_state.json contains old or unredacted field %q:\n%s", forbidden, text)
		}
	}
	for _, want := range []string{`"acceptance_criteria"`, `"required_artifacts"`, `"validation_requirements"`, `"status_reason"`} {
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
		Description:            "finish task",
		AcceptanceCriteria:     []string{"tests pass"},
		RequiredArtifacts:      []string{"artifact.txt"},
		ValidationRequirements: []string{"go test ./..."},
		VerificationMethod:     "tests pass",
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
		!strings.Contains(decision.ContinuePrompt, "tests pass") {
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

func TestGoalStateStoreReadsLegacyGoalStateDefensively(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "goal_state.json"), []byte(`{
  "version": 1,
  "objective": "legacy objective",
  "status": "complete",
  "budget": {"continuations_used": 2},
  "last_progress": "old"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := NewGoalStateStore(dir, GoalStateOptions{}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Description != "legacy objective" || state.Status != GoalStatusSuccess || state.ContinuationCount != 2 {
		t.Fatalf("legacy state = %+v", state)
	}
}

func TestGoalStateProviderContextRendersCompactContract(t *testing.T) {
	state := GoalState{
		Description:            "complete\ndocs",
		AcceptanceCriteria:     []string{"reviewed\nby tester", "published"},
		RequiredArtifacts:      []string{"docs/guide.md"},
		ArtifactRequirements:   []string{"guide explains migration"},
		ValidationRequirements: []string{"markdown links pass"},
		VerificationMethod:     "run docs check",
		Status:                 GoalStatusInProgress,
		StatusReason:           "docs still need review",
	}
	rendered, ok := state.RenderProviderContext()
	if !ok {
		t.Fatal("expected provider context")
	}
	for _, want := range []string{
		"Current goal contract",
		"acceptance criteria",
		"required artifacts",
		"validation requirements",
		"status reason",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("provider context missing %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(rendered, "complete docs") || !strings.Contains(rendered, "reviewed by tester") {
		t.Fatalf("provider context should collapse multiline values:\n%s", rendered)
	}
}
