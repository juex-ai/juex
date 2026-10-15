package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	tasksmodule "github.com/juex-ai/juex/internal/features/tasks"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/tests/testsupport/modulestate"
)

func TestTasksCompletionRespectsNotesModuleAndThreadBoundaries(t *testing.T) {
	for _, tasksEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty-tasks-enabled=%v", tasksEnabled), func(t *testing.T) {
			isolateModuleConfig(t)
			cfg := inputTrackingConfig(t, false)
			cfg.Modules[tasksmodule.ModuleID] = config.ModuleSettings{Enabled: tasksEnabled}
			cfg.Modules[notesmodule.ModuleID] = config.ModuleSettings{Enabled: true}
			a := inputTrackingApp(t, cfg, &bareScriptProvider{steps: []llm.Response{inputTrackingAnswer("Ready.")}})
			_, notes := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
			if _, err := notes.Update("keep without tasks"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Engine.Turn(t.Context(), "Check readiness."); err != nil {
				t.Fatal(err)
			}
			if snapshot, err := notes.Snapshot(); err != nil || snapshot.Content != "keep without tasks" {
				t.Fatalf("empty/disabled Tasks cleared Notes: %+v %v", snapshot, err)
			}
		})
	}
	t.Run("disabled-notes", func(t *testing.T) {
		isolateModuleConfig(t)
		cfg := inputTrackingConfig(t, false)
		cfg.Modules[tasksmodule.ModuleID] = config.ModuleSettings{Enabled: true}
		cfg.Modules[notesmodule.ModuleID] = config.ModuleSettings{Enabled: false}
		a := inputTrackingApp(t, cfg, &bareScriptProvider{})
		create, _ := a.Engine.Tools.Get(tasksmodule.ToolCreate)
		if _, err := create.Handler(t.Context(), map[string]any{"title": "done", "description": "verified", "status": "done"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(a.Thread.Dir, "modules", "notes")); !os.IsNotExist(err) {
			t.Fatalf("disabled Notes created resources: %v", err)
		}
	})
	t.Run("worker-and-new-notes", func(t *testing.T) {
		isolateModuleConfig(t)
		cfg := inputTrackingConfig(t, false)
		cfg.Modules[tasksmodule.ModuleID] = config.ModuleSettings{Enabled: true}
		cfg.Modules[notesmodule.ModuleID] = config.ModuleSettings{Enabled: true}
		main := inputTrackingApp(t, cfg, &bareScriptProvider{})
		_, mainNotes := modulestate.Stores(main.Engine.ThreadRuntimeSnapshot().Modules)
		if _, err := mainNotes.Update("Main notes"); err != nil {
			t.Fatal(err)
		}
		stored, err := thread.NewStore(cfg.AgentStateDir).CreateWorker(thread.MainID, "notes-worker", 2)
		if err != nil {
			t.Fatal(err)
		}
		id := stored.ID
		if err := stored.Close(); err != nil {
			t.Fatal(err)
		}
		provider := &bareScriptProvider{steps: []llm.Response{
			{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				memoryCall("old-notes", notesmodule.ToolUpdate, map[string]any{"content": "old worker notes"}),
				memoryCall("done", tasksmodule.ToolCreate, map[string]any{"title": "finished", "description": "verified", "status": "done"}),
				memoryCall("new-task", tasksmodule.ToolCreate, map[string]any{"title": "new work", "description": "needs input", "status": "pending"}),
				memoryCall("new-notes", notesmodule.ToolUpdate, map[string]any{"content": "new worker notes"}),
			}}, StopReason: llm.StopToolUse},
			inputTrackingAnswer("Waiting on new work."),
		}}
		worker, err := app.New(app.Options{Config: cfg, ThreadID: id, Provider: provider, DisableMCP: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = worker.CloseAndWait() })
		var contents []string
		unsubscribe := worker.Engine.Bus.Subscribe("notes.updated", func(event events.Event) {
			contents = append(contents, event.Payload.(notesmodule.NotesUpdatedPayload).Content)
		})
		defer unsubscribe()
		if _, err := worker.Engine.Turn(t.Context(), "Finish existing work and record new work."); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(contents, []string{"old worker notes", "", "new worker notes"}) {
			t.Fatalf("Notes tool order: %v", contents)
		}
		_, workerNotes := modulestate.Stores(worker.Engine.ThreadRuntimeSnapshot().Modules)
		for store, want := range map[*notesmodule.NotesStore]string{mainNotes: "Main notes", workerNotes: "new worker notes"} {
			if snapshot, err := store.Snapshot(); err != nil || snapshot.Content != want {
				t.Fatalf("Thread Notes=%+v want=%q error=%v", snapshot, want, err)
			}
		}
	})
}

func TestTasksCompletionClearsNotesBeforeNextProviderIteration(t *testing.T) {
	isolateModuleConfig(t)
	cfg := inputTrackingConfig(t, false)
	cfg.Modules[tasksmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	cfg.Modules[notesmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	provider := &inputTrackingProvider{}
	a := inputTrackingApp(t, cfg, provider)
	tasks, notes := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
	first, err := tasks.Create(tasksmodule.Create{Title: "First", Description: "first task", Status: tasksmodule.Doing})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tasks.Create(tasksmodule.Create{Title: "Second", Description: "second task", Status: tasksmodule.Pending})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("completion-notes-fixture"); err != nil {
		t.Fatal(err)
	}
	var cleared []events.Event
	unsubscribe := a.Engine.Bus.Subscribe("notes.updated", func(event events.Event) {
		payload, ok := event.Payload.(notesmodule.NotesUpdatedPayload)
		if ok && payload.Content == "" {
			cleared = append(cleared, event)
		}
	})
	defer unsubscribe()
	calls := 0
	provider.complete = func(_ context.Context, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
		calls++
		visible := false
		for _, message := range history {
			if message.ID == "runtime-notes" && strings.Contains(message.FirstText(), "completion-notes-fixture") {
				visible = true
			}
		}
		if visible != (calls < 3) {
			t.Fatalf("iteration %d has stale or missing Notes: visible=%v", calls, visible)
		}
		if calls <= 2 {
			id := first.ID
			if calls == 2 {
				id = second.ID
			}
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall(fmt.Sprint(calls), tasksmodule.ToolUpdate, map[string]any{"id": id, "status": "done"})}}, StopReason: llm.StopToolUse}, nil
		}
		return inputTrackingAnswer("All tasks completed."), nil
	}
	if _, err := a.Engine.Turn(t.Context(), "Complete both tasks."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(notes.Path); !os.IsNotExist(err) {
		t.Fatalf("completed Notes remain on disk: %v", err)
	}
	if len(cleared) != 1 || cleared[0].TurnID == "" {
		t.Fatalf("missing single current-Turn Notes invalidation: %+v", cleared)
	}
	state, err := tasks.Snapshot()
	if err != nil || len(state.Tasks) != 2 || state.Tasks[0].Status != tasksmodule.Done || state.Tasks[1].Status != tasksmodule.Done {
		t.Fatalf("completion removed task records: %+v %v", state, err)
	}
}

func TestTasksCompletionRetriesNotesCleanupAfterRenewal(t *testing.T) {
	isolateModuleConfig(t)
	cfg := inputTrackingConfig(t, false)
	cfg.Modules[tasksmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	cfg.Modules[notesmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	a := inputTrackingApp(t, cfg, &bareScriptProvider{})
	tasks, notes := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
	task, err := tasks.Create(tasksmodule.Create{Title: "finish", Description: "verified", Status: tasksmodule.Doing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("recoverable notes"); err != nil {
		t.Fatal(err)
	}
	_, rollback, err := notes.StageClearForContextRenewal("g000002")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rollback() })
	update, _ := a.Engine.Tools.Get(tasksmodule.ToolUpdate)
	input := map[string]any{"id": task.ID, "status": "done"}
	if _, err := update.Handler(t.Context(), input); err == nil || !strings.Contains(err.Error(), "task change was saved") || !strings.Contains(err.Error(), "notes clear") {
		t.Fatalf("missing partial-success error: %v", err)
	}
	state, err := tasks.Snapshot()
	if err != nil || state.Tasks[0].Status != tasksmodule.Done {
		t.Fatalf("task completion lost: %+v %v", state, err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := update.Handler(t.Context(), input); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	if snapshot, err := notes.StatusSnapshot(); err != nil || snapshot != nil {
		t.Fatalf("retry left Notes: %+v %v", snapshot, err)
	}
}

func TestTasksTakeOverCheckedInputAndPruneAtGenerationBoundaries(t *testing.T) {
	isolateModuleConfig(t)
	cfg := inputTrackingConfig(t, true)
	cfg.Modules[tasksmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	provider := &inputTrackingProvider{}
	a := inputTrackingApp(t, cfg, provider)
	store, _ := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
	call := 0
	provider.complete = func(ctx context.Context, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
		call++
		tool := func(name string, input map[string]any) llm.Response {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall(fmt.Sprint(call), name, input)}}, StopReason: llm.StopToolUse}
		}
		switch call {
		case 1:
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				memoryCall("create-a", tasksmodule.ToolCreate, map[string]any{"title": "Active work", "description": "finish active request", "acceptance": "verified", "status": "doing", "priority": "p2"}),
				memoryCall("create-b", tasksmodule.ToolCreate, map[string]any{"title": "Urgent queued work", "description": "finish queued request", "acceptance": "verified", "priority": "p0"}),
				memoryCall("create-c", tasksmodule.ToolCreate, map[string]any{"title": "Waiting", "description": "requires external input", "status": "pending", "priority": "p0"}),
			}}, StopReason: llm.StopToolUse}, nil
		case 2:
			state, err := store.Snapshot()
			if err != nil || len(state.Tasks) != 3 {
				t.Fatalf("tasks not durable before check: %+v %v", state, err)
			}
			inputs, err := a.Engine.UncheckedInputs(ctx)
			if err != nil || len(inputs) != 1 {
				t.Fatalf("inputs: %+v %v", inputs, err)
			}
			return inputTrackingCheck(inputs[0].ID), nil
		case 3, 5, 7:
			if reminders := inputTrackingReminders(history); reminders != "" {
				t.Fatalf("checked input returned: %s", reminders)
			}
			return inputTrackingAnswer("Progress recorded."), nil
		case 4, 6:
			state, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			index := (call - 4) / 2
			selected := state.Tasks[index]
			var continuation string
			for _, message := range history {
				if message.Kind == llm.MessageKindContinuation {
					continuation = message.FirstText()
				}
			}
			if !strings.Contains(continuation, selected.ID) {
				t.Fatalf("wrong task selected: %s", continuation)
			}
			return tool(tasksmodule.ToolUpdate, map[string]any{"id": selected.ID, "status": "done", "status_reason": "acceptance verified"}), nil
		default:
			return llm.Response{}, fmt.Errorf("unexpected call %d", call)
		}
	}
	if _, err := a.Engine.Turn(t.Context(), "Finish active and queued requests; retain the waiting request."); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil || len(state.Tasks) != 3 {
		t.Fatalf("tasks: %+v %v", state, err)
	}
	if state.Tasks[0].ContinuationCount != 1 || state.Tasks[1].ContinuationCount != 1 || state.Tasks[2].ContinuationCount != 0 {
		t.Fatalf("per-task continuation: %+v", state)
	}
	pending := state.Tasks[2]
	retained := []tasksmodule.Task{pending}
	for _, status := range []tasksmodule.Status{tasksmodule.Todo, tasksmodule.Doing, tasksmodule.Failed} {
		task, err := store.Create(tasksmodule.Create{Title: string(status), Description: "retain across Generations", Status: status})
		if err != nil {
			t.Fatal(err)
		}
		retained = append(retained, task)
	}
	if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
		t.Fatal(err)
	}
	after, err := store.Snapshot()
	if err != nil || !reflect.DeepEqual(after.Tasks, retained) {
		t.Fatalf("compact prune: %+v %v", after, err)
	}
	if _, err := store.Create(tasksmodule.Create{Title: "Completed", Description: "verified", Status: tasksmodule.Done}); err != nil {
		t.Fatal(err)
	}
	if err := a.NewContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err = store.Snapshot()
	if err != nil || !reflect.DeepEqual(after.Tasks, retained) {
		t.Fatalf("new prune: %+v %v", after, err)
	}
}
