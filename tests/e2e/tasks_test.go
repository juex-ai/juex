package e2e

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	tasksmodule "github.com/juex-ai/juex/internal/features/tasks"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/tests/testsupport/modulestate"
)

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
