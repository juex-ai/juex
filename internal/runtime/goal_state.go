package runtime

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/events"
)

func (e *Engine) goalStateStoreLocked() *GoalStateStore {
	return e.currentGoalStateStore()
}

func goalStateRawFromStore(store *GoalStateStore) (json.RawMessage, bool) {
	if store == nil {
		return nil, false
	}
	state, err := store.Snapshot()
	if err != nil {
		return nil, false
	}
	raw := state.RawMessage()
	if len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

func (e *Engine) GoalStatusSnapshot() (*GoalStatusSnapshot, error) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
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

func (e *Engine) emitGoalUpdated(turnID string) {
	if e == nil {
		return
	}
	store := e.goalStateStoreLocked()
	if store == nil {
		return
	}
	snapshot, err := store.StatusSnapshot()
	if err != nil || snapshot == nil {
		return
	}
	e.emit(events.Event{Type: "goal.updated", TurnID: turnID, Payload: goalUpdatedPayload(snapshot)})
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
