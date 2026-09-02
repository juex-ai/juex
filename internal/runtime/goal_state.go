package runtime

import (
	"context"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func (m *GoalModule) ClearContextForRenewal(context.Context) (func() error, error) {
	if m == nil || m.store == nil {
		return func() error { return nil }, nil
	}
	return m.store.ClearWithRollback()
}

func (m *GoalModule) GoalStateStore() *workmem.GoalStateStore {
	if m == nil {
		return nil
	}
	return m.store
}

func (m *GoalModule) GoalStatusSnapshot() (*workmem.GoalStatusSnapshot, error) {
	store := m.GoalStateStore()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (m *GoalModule) HookGoalState() []byte {
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

func (m *GoalModule) GoalCompactionState() (*CompactionSummaryGoal, error) {
	store := m.GoalStateStore()
	if store == nil {
		return nil, nil
	}
	state, err := store.Snapshot()
	if err != nil {
		return nil, fmt.Errorf("goal state: %w", err)
	}
	snapshot := state.StatusSnapshot()
	if snapshot == nil {
		return nil, nil
	}
	return &CompactionSummaryGoal{
		Description:  snapshot.Description,
		Acceptance:   snapshot.Acceptance,
		Status:       string(snapshot.Status),
		StatusReason: snapshot.StatusReason,
	}, nil
}

func goalStateContextFromStore(store *workmem.GoalStateStore) (string, bool) {
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (m *GoalModule) emitGoalUpdated(turnID string) {
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

func (m *GoalModule) activeTurnID() string {
	if m == nil || m.currentTurnID == nil {
		return ""
	}
	return m.currentTurnID()
}

func (m *GoalModule) emit(event events.Event) error {
	if m == nil || m.eventSink == nil {
		return nil
	}
	return m.eventSink(event)
}

func goalUpdatedPayload(snapshot *workmem.GoalStatusSnapshot) GoalUpdatedPayload {
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

func goalContinuedPayload(decision workmem.GoalGateDecision, snapshot *workmem.GoalStatusSnapshot) GoalContinuedPayload {
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
