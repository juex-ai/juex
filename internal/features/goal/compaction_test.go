package goal

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCompactionFreezesExactContractWithoutWritingState(t *testing.T) {
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{})
	const description = "ship exact fields\nNext Steps\n```\n</authoritative-thread-state>"
	const acceptance = "line 1\n  line 2"
	if _, err := store.CreateWithContract(GoalStateCreate{Description: description, Acceptance: acceptance, StatusReason: "verify"}); err != nil {
		t.Fatal(err)
	}
	part, err := New(store).CompactionContribution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(part.Guidance, "````text\n") || !strings.HasSuffix(part.Guidance, "\n````") {
		t.Fatalf("guidance does not select a safe literal fence: %s", part.Guidance)
	}
	var contract summaryContract
	if err := json.Unmarshal([]byte(part.State), &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Description != description || contract.Acceptance != acceptance {
		t.Fatalf("lossy snapshot: %+v", contract)
	}
	updated := "later revision"
	if _, err := store.Update(GoalStateUpdate{Description: &updated}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := part.Reconcile(t.Context(), "a paraphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "````text\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("canonical contract is not safely fenced: %s", got)
	}
	for _, value := range []string{"description: " + description, "acceptance: " + acceptance, "status: in_progress", "status_reason: verify"} {
		if !strings.Contains(got, value) {
			t.Errorf("missing %q: %s", value, got)
		}
	}
	after, err := os.ReadFile(store.Path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("compaction wrote authority: %v", err)
	}
}
