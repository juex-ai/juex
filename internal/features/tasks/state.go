package tasks

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/events"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func (m *Module) StageContextTransition(_ context.Context, _ runtimemodule.ContextTransitionKind, generationID string) (runtimemodule.ContextRenewalClear, error) {
	if m == nil || m.store == nil {
		return runtimemodule.ContextRenewalClear{
			Finalize: func() error { return nil },
			Rollback: func() error { return nil },
		}, nil
	}
	finalize, rollback, err := m.store.StagePruneDone(generationID)
	return runtimemodule.ContextRenewalClear{Finalize: finalize, Rollback: rollback}, err
}

func (m *Module) Store() *Store {
	if m == nil {
		return nil
	}
	return m.store
}

func (m *Module) Snapshot() (*TasksSnapshot, error) {
	store := m.Store()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (m *Module) HookTasksState() []byte {
	store := m.Store()
	if store == nil {
		return nil
	}
	state, err := store.Snapshot()
	if err != nil {
		return nil
	}
	return state.RawMessage()
}

func tasksStateContextFromStore(store *Store) (string, bool) {
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (m *Module) emitTasksUpdated(turnID string) {
	if m == nil {
		return
	}
	store := m.store
	if store == nil {
		return
	}
	snapshot, err := store.StatusSnapshot()
	if err != nil {
		return
	}
	_ = m.emit(events.Event{Type: "tasks.updated", TurnID: turnID, Payload: tasksUpdatedPayload(snapshot)})
}

func (m *Module) activeTurnID() string {
	if m == nil || m.currentTurnID == nil {
		return ""
	}
	return m.currentTurnID()
}

func (m *Module) emit(event events.Event) error {
	if m == nil || m.eventSink == nil {
		return nil
	}
	return m.eventSink(event)
}

func tasksUpdatedPayload(snapshot *TasksSnapshot) TasksUpdatedPayload {
	if snapshot == nil {
		return TasksUpdatedPayload{Tasks: []Task{}}
	}
	return TasksUpdatedPayload{Tasks: snapshot.Tasks}
}
func tasksContinuedPayload(decision GateDecision, snapshot *TasksSnapshot) TaskContinuedPayload {
	task := decision.Task
	if snapshot != nil {
		for _, current := range snapshot.Tasks {
			if current.ID == task.ID {
				task = current
				break
			}
		}
	}
	return TaskContinuedPayload{TaskID: task.ID, Status: task.Status, Reason: decision.Reason, ContinuationCount: task.ContinuationCount, ContinuationPromptLen: len(decision.ContinuePrompt)}
}
