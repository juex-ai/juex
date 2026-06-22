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

	state, err := store.Create("ship feature with api_key=secret", "run focused tests")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusInProgress || state.Description == "" {
		t.Fatalf("created state = %+v", state)
	}
	if strings.Contains(state.Description, "secret") {
		t.Fatalf("description not redacted: %q", state.Description)
	}

	state, err = store.Update(GoalStateUpdate{Status: GoalStatusSuccess})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusSuccess {
		t.Fatalf("status = %q", state.Status)
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

	if _, err := store.Create("finish task", "tests pass"); err != nil {
		t.Fatal(err)
	}
	decision, err = store.CompletionGateDecision()
	if err != nil {
		t.Fatal(err)
	}
	if !decision.BlockStop || decision.Reason != "goal_in_progress" || !strings.Contains(decision.ContinuePrompt, "finish task") {
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
