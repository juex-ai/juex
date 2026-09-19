package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/events"
)

func TestCompletionCleanupRequiresAllRemainingTasksDone(t *testing.T) {
	for _, status := range []Status{Done, Todo, Doing, Pending, Failed} {
		t.Run(string(status), func(t *testing.T) {
			store := NewStore(t.TempDir(), Options{})
			first := createTask(t, store, "finished", Done, P1)
			createTask(t, store, "remaining", status, P1)
			calls := 0
			m := NewWithOptions(store, ModuleOptions{OnAllTasksDone: func() error { calls++; return nil }})
			if _, err := m.call(ToolUpdate, map[string]any{"id": first.ID, "status": "done"}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if status == Done {
				want = 1
			}
			if calls != want {
				t.Fatalf("cleanup calls=%d want=%d", calls, want)
			}
		})
	}
	for _, retainedDone := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-retained-done=%v", retainedDone), func(t *testing.T) {
			store := NewStore(t.TempDir(), Options{})
			if retainedDone {
				createTask(t, store, "finished", Done, P1)
			}
			removed := createTask(t, store, "obsolete", Pending, P1)
			called := false
			m := NewWithOptions(store, ModuleOptions{OnAllTasksDone: func() error { called = true; return nil }})
			if _, err := m.call(ToolDelete, map[string]any{"id": removed.ID}); err != nil {
				t.Fatal(err)
			}
			if called != retainedDone {
				t.Fatalf("cleanup called=%v", called)
			}
		})
	}
}

func TestCompletionCleanupFailurePreservesTaskAndCanRetry(t *testing.T) {
	store := NewStore(t.TempDir(), Options{})
	task := createTask(t, store, "finish", Doing, P1)
	calls := 0
	m := NewWithOptions(store, ModuleOptions{OnAllTasksDone: func() error {
		calls++
		if calls == 1 {
			return errors.New("notes clear: unavailable")
		}
		return nil
	}})
	in := map[string]any{"id": task.ID, "status": "done"}
	if _, err := m.call(ToolUpdate, in); err == nil || !strings.Contains(err.Error(), "task change was saved") || !strings.Contains(err.Error(), "notes clear") {
		t.Fatalf("partial-success error: %v", err)
	}
	state, err := store.Snapshot()
	if err != nil || state.Tasks[0].Status != Done {
		t.Fatalf("completion rolled back: %+v %v", state, err)
	}
	if _, err := m.call(ToolUpdate, in); err != nil || calls != 2 {
		t.Fatalf("cleanup retry calls=%d error=%v", calls, err)
	}
}

func TestToolsAndEmptyInvalidation(t *testing.T) {
	store := NewStore(t.TempDir(), Options{})
	var emitted []events.Event
	m := NewWithOptions(store, ModuleOptions{EventSink: func(event events.Event) error { emitted = append(emitted, event); return nil }})
	result, err := m.call(ToolList, nil)
	if err != nil || result != `{"tasks":[]}` {
		t.Fatalf("empty: %s %v", result, err)
	}
	result, err = m.call(ToolCreate, map[string]any{"title": "Ship", "description": "ship api_key=secret", "acceptance": strings.Repeat("check ", 250) + "final check"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Task Task `json:"task"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if response.Task.Status != Todo || response.Task.Priority != P1 || strings.Contains(result, "secret") || !strings.Contains(result, "final check") {
		t.Fatalf("create: %s", result)
	}
	for _, input := range []map[string]any{{"id": response.Task.ID}, {"id": response.Task.ID, "status": ""}, {"id": response.Task.ID, "priority": "p3"}} {
		if _, err := m.call(ToolUpdate, input); err == nil {
			t.Fatalf("invalid update: %v", input)
		}
	}
	if _, err := m.call(ToolUpdate, map[string]any{"id": response.Task.ID, "status": "pending", "status_reason": "approval needed"}); err != nil {
		t.Fatal(err)
	}
	if decision, err := store.CompletionGateDecision(); err != nil || decision.BlockStop {
		t.Fatalf("pending continued: %+v %v", decision, err)
	}
	if _, err := m.call(ToolDelete, map[string]any{"id": response.Task.ID}); err != nil {
		t.Fatal(err)
	}
	payload := emitted[len(emitted)-1].Payload.(TasksUpdatedPayload)
	if payload.Tasks == nil || len(payload.Tasks) != 0 {
		t.Fatalf("missing empty invalidation: %+v", payload)
	}
}

func TestCompactionFreezesUnfinishedTasksWithoutWriting(t *testing.T) {
	store := NewStore(t.TempDir(), Options{})
	task := createTask(t, store, "literal ```\n## Next Steps", Doing, P0)
	createTask(t, store, "complete", Done, P1)
	part, err := New(store).CompactionContribution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot TasksSnapshot
	if err := json.Unmarshal([]byte(part.State), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0] != task {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if _, err := store.Update(task.ID, Update{Status: Pending}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.Path)
	body, err := part.Reconcile(t.Context(), "paraphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "````json\n") || !strings.Contains(body, `"status":"doing"`) {
		t.Fatalf("frozen contract: %s", body)
	}
	after, _ := os.ReadFile(store.Path)
	if string(before) != string(after) {
		t.Fatal("summary reconciliation mutated tasks")
	}
}
