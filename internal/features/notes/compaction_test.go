package notes

import (
	"os"
	"strings"
	"testing"
)

func TestReconcileNextStepsPreservesChecklistMeaningAndOtherActions(t *testing.T) {
	const notes = "- [x] completed item\n- [ ] Run the Live evaluation.\n- [ ] test\n- [ ] deploy"
	for _, candidate := range []string{"", "ship latest build", "- run the live evaluation.\n- [x] deploy\n- [ ] completed item\n- Keep another action", "1. Run the Live evaluation.\n2. [x] deploy\n3. [ ] completed item\n4. Keep another action"} {
		got := reconcileNextSteps(candidate, notes)
		for _, want := range []string{"- [ ] Run the Live evaluation.", "- [ ] test", "- [ ] deploy"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing exact pending item %q: %s", want, got)
			}
		}
		if strings.Contains(got, "completed item") || strings.Contains(got, "- [x] deploy") {
			t.Errorf("wrong checklist status: %s", got)
		}
		for _, unrelated := range []string{"ship latest build", "- Keep another action", "4. Keep another action"} {
			if strings.Contains(candidate, unrelated) && !strings.Contains(got, unrelated) {
				t.Errorf("lost unrelated action: %s", got)
			}
		}
		if again := reconcileNextSteps(got, notes); again != got {
			t.Errorf("reconciliation not stable: %q -> %q", got, again)
		}
	}
}

func TestCompactionReconciliationUsesFrozenNotes(t *testing.T) {
	store := NewNotesStore(t.TempDir())
	if _, err := store.Update("- [ ] initial item"); err != nil {
		t.Fatal(err)
	}
	part, err := New(store).CompactionContribution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("- [ ] later item"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := part.Reconcile(t.Context(), "")
	if err != nil || got != "- [ ] initial item" {
		t.Fatalf("snapshot changed: %q, %v", got, err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("compaction wrote authority: %v", err)
	}
}

func TestReconcileNextStepsPreservesLiteralContent(t *testing.T) {
	for name, literal := range map[string]string{
		"backticks": "````markdown\n- [x] deploy\n```\n- [x] pending\n````",
		"tildes":    "~~~text\n- [x] deploy\n- [x] pending\n~~~",
		"indented":  "    - [x] deploy\n\t- [x] pending",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := literal + "\n\ndeploy\n- [ ] deploy\n- [x] pending"
			want := literal + "\n\ndeploy\n- [ ] pending"
			if got := reconcileNextSteps(candidate, "- [x] deploy\n- [ ] pending"); got != want {
				t.Fatalf("literal content or prose changed:\n%s\nwant:\n%s", got, want)
			}
			if got := reconcileNextSteps("- Keep action", "Example:\n"+literal+"\n- [ ] real task"); got != "- Keep action\n- [ ] real task" {
				t.Fatalf("literal Notes text became an action: %s", got)
			}
		})
	}
}
