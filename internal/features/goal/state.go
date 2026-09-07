package goal

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/events"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func (m *Module) ClearContextForRenewal(_ context.Context, generationID string) (runtimemodule.ContextRenewalClear, error) {
	if m == nil || m.store == nil {
		return runtimemodule.ContextRenewalClear{
			Finalize: func() error { return nil },
			Rollback: func() error { return nil },
		}, nil
	}
	finalize, rollback, err := m.store.StageClearForContextRenewal(generationID)
	return runtimemodule.ContextRenewalClear{Finalize: finalize, Rollback: rollback}, err
}

func (m *Module) GoalStateStore() *GoalStateStore {
	if m == nil {
		return nil
	}
	return m.store
}

func (m *Module) GoalStatusSnapshot() (*GoalStatusSnapshot, error) {
	store := m.GoalStateStore()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (m *Module) HookGoalState() []byte {
	store := m.GoalStateStore()
	if store == nil {
		return nil
	}
	state, err := store.Snapshot()
	if err != nil {
		return nil
	}
	return state.RawMessage()
}

func goalStateContextFromStore(store *GoalStateStore) (string, bool) {
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (m *Module) emitGoalUpdated(turnID string) {
	if m == nil {
		return
	}
	store := m.store
	if store == nil {
		return
	}
	snapshot, err := store.StatusSnapshot()
	if err != nil || snapshot == nil {
		return
	}
	_ = m.emit(events.Event{Type: "goal.updated", TurnID: turnID, Payload: goalUpdatedPayload(snapshot)})
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

func goalUpdatedPayload(snapshot *GoalStatusSnapshot) GoalUpdatedPayload {
	if snapshot == nil {
		return GoalUpdatedPayload{}
	}
	return GoalUpdatedPayload{
		Description:       snapshot.Description,
		Acceptance:        snapshot.Acceptance,
		ContinuationCount: snapshot.ContinuationCount,
		Status:            snapshot.Status,
		StatusReason:      snapshot.StatusReason,
		UpdatedAt:         snapshot.UpdatedAt,
	}
}

func goalContinuedPayload(decision GoalGateDecision, snapshot *GoalStatusSnapshot) GoalContinuedPayload {
	count := decision.ContinuationCount
	if snapshot != nil {
		count = snapshot.ContinuationCount
	}
	return GoalContinuedPayload{
		Status:                decision.Status,
		Reason:                decision.Reason,
		ContinuationCount:     count,
		ContinuationPromptLen: len(decision.ContinuePrompt),
	}
}
