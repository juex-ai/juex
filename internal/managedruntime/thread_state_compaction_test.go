package managedruntime

import (
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestThreadStateCompactionReconcilesLargeAuthoritativeState(t *testing.T) {
	state := ThreadState{Revision: 7, Notes: ThreadNotes{Content: "- [ ] Keep EXACT_439\n- [x] Finished_527\n```\n- [ ] literal example\n```"}, Tasks: []ThreadTask{
		{ID: "pending", Title: "Keep", Description: "Continue", Acceptance: strings.Repeat("x", 5000), Status: "pending", Priority: "p0", UpdatedAt: time.Now()},
		{ID: "done", Title: "Complete", Description: "Do not revive", Status: "done", Priority: "p1", UpdatedAt: time.Now()},
	}}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	draft := &CompactionDraft{ThreadState: state, NotesEnabled: true, TasksEnabled: true, BeforeTokens: 20000}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Tasks\nEverything done.\n\n## Next Steps\n- Finished_527\n- Another step"), StopReason: llm.StopEndTurn}
	request := ModelRequest{Compaction: draft, MaxOutputTokens: 128, Model: ModelConfig{ContextWindow: 32000, OutputReserve: 2000}}
	if err := ValidateCompactionSummary(request, response); err != nil {
		t.Fatal("runtime-owned facts incorrectly count against provider output cap", err)
	}
	text := draft.Reconcile(CompactionText(response))
	if !strings.Contains(text, state.Tasks[0].Acceptance) || !strings.Contains(text, "- [ ] Keep EXACT_439") || !strings.Contains(text, "Another step") || strings.Contains(text, "Finished_527") || strings.Contains(text, "literal example") || strings.Contains(text, "Do not revive") {
		t.Fatal("authoritative reconciliation failed", text[:min(500, len(text))])
	}
	if len(state.Tasks) != 2 || state.Revision != 7 {
		t.Fatal("planning modified durable state")
	}
	request.Model.ContextWindow = 2048
	if err := ValidateCompactionSummary(request, response); err == nil {
		t.Fatal("reconciled context budget was not checked")
	}
}

func TestThreadStateNotesIgnoreLiteralCheckboxes(t *testing.T) {
	notes := "- parent\n  - [ ] nested real\n  ```\n  - [ ] fenced example\n  ```\n\t\t- [ ] code example\n- [x] finished"
	got := reconcileNextSteps("- finished\n- unrelated", notes)
	if !strings.Contains(got, "nested real") || !strings.Contains(got, "unrelated") || strings.Contains(got, "example") || strings.Contains(got, "finished") {
		t.Fatal(got)
	}
}

func TestThreadStateContinuationRejectsStaleTask(t *testing.T) {
	state := ThreadState{Tasks: []ThreadTask{{ID: "a", Title: "A", Description: "A", Status: "doing", Priority: "p0", UpdatedAt: time.Now()}}}
	selected, _ := state.SelectedTask()
	state.Tasks[0].Acceptance = "New acceptance"
	if _, err := state.ContinueTask(selected, time.Now()); err == nil {
		t.Fatal("stale decision committed")
	}
	if state.Tasks[0].ContinuationCount != 0 {
		t.Fatal("failed decision mutated state")
	}
}
