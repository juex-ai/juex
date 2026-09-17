package modulestate

import (
	"encoding/json"
	"fmt"
	"github.com/juex-ai/juex/internal/features/notes"
	"github.com/juex-ai/juex/internal/features/tasks"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"strings"
	"testing"
)

func Stores(set *runtimemodule.Set) (*tasks.Store, *notes.NotesStore) {
	return tasks.StoreFromModules(set), notes.StoreFromModules(set)
}
func Status(set *runtimemodule.Set) (*tasks.TasksSnapshot, *notes.NotesSnapshot) {
	g, _ := tasks.StatusFromModules(set)
	n, _ := notes.StatusFromModules(set)
	return g, n
}

func First(t *testing.T, store *tasks.Store) tasks.Task {
	t.Helper()
	state, err := store.Snapshot()
	if err != nil || len(state.Tasks) == 0 {
		t.Fatalf("task fixture missing: %+v %v", state, err)
	}
	return state.Tasks[0]
}

// ResolveTaskFixture reads the task ID from the actual provider context, just
// as the scripted model would. Only explicit $first_task placeholders resolve.
func ResolveTaskFixture(history []llm.Message, response llm.Response) (llm.Response, error) {
	for i := range response.Message.Blocks {
		block := &response.Message.Blocks[i]
		if block.Input["id"] != "$first_task" {
			continue
		}
		var id string
		for _, message := range history {
			if message.Kind != llm.MessageKindRuntimeContext {
				continue
			}
			for _, line := range strings.Split(message.FirstText(), "\n") {
				var task tasks.Task
				if json.Unmarshal([]byte(line), &task) == nil && task.ID != "" {
					id = task.ID
					break
				}
			}
		}
		if id == "" {
			return response, fmt.Errorf("task fixture has no visible task ID")
		}
		input := make(map[string]any, len(block.Input))
		for k, v := range block.Input {
			input[k] = v
		}
		input["id"] = id
		block.Input = input
	}
	return response, nil
}
