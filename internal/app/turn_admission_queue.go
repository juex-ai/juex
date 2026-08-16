package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

var errTurnAdmissionBusy = errors.New("app: session busy")
var errTurnAdmissionChanged = errors.New("app: turn changed while accepting input")

const maxTurnAdmissionAttempts = 2

type turnAdmissionRuntime interface {
	AdmitTurnMessage(string, llm.Message) (llm.Message, error)
	ReserveCompactionTurnID(string) error
	EnqueuePendingMessage(context.Context, llm.Message) (runtime.PendingInputStatus, error)
	EnqueuePersistedPendingMessage(context.Context, runtime.PendingInputRecord) (runtime.PendingInputStatus, error)
	PromotePendingInputTurn(string, string) (llm.Message, runtime.PendingInputStatus, bool, error)
}

type turnAdmissionQueue struct {
	state  *turnAdmission
	engine turnAdmissionRuntime
}

func (a *App) admissionQueue() turnAdmissionQueue {
	if a == nil || a.Engine == nil {
		return turnAdmissionQueue{}
	}
	return turnAdmissionQueue{state: &a.turnAdmission, engine: a.Engine}
}

func (q turnAdmissionQueue) admitUser(ctx context.Context, msg llm.Message, ids TurnIDAllocator) TurnAdmissionResult {
	if q.state == nil || q.engine == nil {
		return errorResult(fmt.Errorf("turn admission: app, engine, or session is not initialized"), nil)
	}
	if ids == nil {
		return errorResult(fmt.Errorf("turn admission: missing turn id allocator"), nil)
	}
	var lastStatus runtime.PendingInputStatus
	// A runtime-owned turn can finish between reserve and enqueue. Reconcile
	// that transition once without making the App own the external turn.
	for attempt := 0; attempt < maxTurnAdmissionAttempts; attempt++ {
		result, status, retry := q.admitUserAttempt(ctx, msg, ids)
		if !retry {
			return result
		}
		lastStatus = status
	}
	return conflictResult(
		"turn changed while accepting input; retry the message",
		errTurnAdmissionChanged,
		lastStatus,
	)
}

func (q turnAdmissionQueue) admitPersisted(ctx context.Context, record runtime.PendingInputRecord, ids TurnIDAllocator) TurnAdmissionResult {
	if q.state == nil || q.engine == nil {
		return errorResult(fmt.Errorf("turn admission: app, engine, or session is not initialized"), nil)
	}
	if ids == nil {
		return errorResult(fmt.Errorf("turn admission: missing turn id allocator"), nil)
	}
	var lastStatus runtime.PendingInputStatus
	for attempt := 0; attempt < maxTurnAdmissionAttempts; attempt++ {
		result, status, retry := q.admitPersistedAttempt(ctx, record, ids)
		if !retry {
			return result
		}
		lastStatus = status
	}
	return conflictResult("turn changed while accepting input; retry the message", errTurnAdmissionChanged, lastStatus)
}

func (q turnAdmissionQueue) admitPersistedAttempt(ctx context.Context, record runtime.PendingInputRecord, ids TurnIDAllocator) (TurnAdmissionResult, runtime.PendingInputStatus, bool) {
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	phase, activeTurnID := q.snapshot()
	if phase != turnAdmissionIdle {
		return q.queuePersisted(ctx, record, phase, activeTurnID)
	}
	// The runtime owns durable-record freshness. Even while the App appears
	// idle, let it reject expired records or discover a runtime-owned turn
	// before reserving a new one.
	if result, status, retry := q.queuePersisted(ctx, record, phase, activeTurnID); !retry {
		return result, status, false
	}
	turnID := ids.NextTurnID("turn")
	accepted, err := q.engine.AdmitTurnMessage(turnID, record.Message)
	if err != nil {
		if errors.Is(err, runtime.ErrActiveTurnExists) {
			return q.queuePersisted(ctx, record, turnAdmissionIdle, "")
		}
		return conflictResult(err.Error(), err, runtime.PendingInputStatus{}), runtime.PendingInputStatus{}, false
	}
	q.state.mu.Lock()
	q.state.phase = turnAdmissionRunning
	q.state.turnID = turnID
	q.state.mu.Unlock()
	return TurnAdmissionResult{
		Kind:   TurnAdmissionStarted,
		TurnID: turnID,
		Start:  &AdmittedTurn{TurnID: turnID, Message: accepted},
	}, runtime.PendingInputStatus{TurnID: turnID}, false
}

func (q turnAdmissionQueue) admitUserAttempt(
	ctx context.Context,
	msg llm.Message,
	ids TurnIDAllocator,
) (TurnAdmissionResult, runtime.PendingInputStatus, bool) {
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	phase, activeTurnID := q.snapshot()
	if phase != turnAdmissionIdle {
		return q.queuePending(ctx, msg, phase, activeTurnID)
	}

	turnID := ids.NextTurnID("turn")
	accepted, err := q.engine.AdmitTurnMessage(turnID, msg)
	if err != nil {
		if errors.Is(err, runtime.ErrActiveTurnExists) {
			return q.queuePending(ctx, msg, turnAdmissionIdle, "")
		}
		return conflictResult(err.Error(), err, runtime.PendingInputStatus{}), runtime.PendingInputStatus{}, false
	}
	q.state.mu.Lock()
	q.state.phase = turnAdmissionRunning
	q.state.turnID = turnID
	q.state.mu.Unlock()

	return TurnAdmissionResult{
		Kind:   TurnAdmissionStarted,
		TurnID: turnID,
		Start:  &AdmittedTurn{TurnID: turnID, Message: accepted},
	}, runtime.PendingInputStatus{TurnID: turnID}, false
}

func (q turnAdmissionQueue) complete(turnID string) {
	if q.state == nil || turnID == "" {
		return
	}
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if q.state.phase == turnAdmissionRunning && q.state.turnID == turnID {
		q.state.phase = turnAdmissionIdle
		q.state.turnID = ""
	}
}

func (q turnAdmissionQueue) beginCompact(turnID string) error {
	if q.state == nil || q.engine == nil {
		return runtime.ErrNoActiveTurn
	}
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	q.state.mu.Lock()
	if q.state.phase != turnAdmissionIdle {
		q.state.mu.Unlock()
		return errTurnAdmissionBusy
	}
	q.state.mu.Unlock()
	if err := q.engine.ReserveCompactionTurnID(turnID); err != nil {
		return err
	}
	q.state.mu.Lock()
	q.state.phase = turnAdmissionCompacting
	q.state.turnID = turnID
	q.state.mu.Unlock()
	return nil
}

func (q turnAdmissionQueue) finishCompact(compactTurnID string, ids TurnIDAllocator) (*AdmittedTurn, error) {
	if q.state == nil || q.engine == nil || ids == nil {
		return nil, nil
	}
	nextTurnID := ids.NextTurnID("turn")
	msg, _, promoted, err := q.engine.PromotePendingInputTurn(compactTurnID, nextTurnID)
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if promoted {
		q.state.phase = turnAdmissionRunning
		q.state.turnID = nextTurnID
		return &AdmittedTurn{TurnID: nextTurnID, Message: msg}, nil
	}
	if q.state.phase == turnAdmissionCompacting && q.state.turnID == compactTurnID {
		q.state.phase = turnAdmissionIdle
		q.state.turnID = ""
	}
	return nil, err
}

func (q turnAdmissionQueue) beginExclusiveCommand() bool {
	if q.state == nil {
		return false
	}
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if q.state.phase != turnAdmissionIdle {
		return false
	}
	q.state.phase = turnAdmissionCommand
	return true
}

func (q turnAdmissionQueue) finishExclusiveCommand() {
	if q.state == nil {
		return
	}
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if q.state.phase == turnAdmissionCommand {
		q.state.phase = turnAdmissionIdle
		q.state.turnID = ""
	}
}

func (q turnAdmissionQueue) finishExclusiveCommandAsRunning(turnID string) {
	if q.state == nil {
		return
	}
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if q.state.phase == turnAdmissionCommand {
		q.state.phase = turnAdmissionRunning
		q.state.turnID = turnID
	}
}

func (q turnAdmissionQueue) snapshot() (turnAdmissionPhase, string) {
	if q.state == nil {
		return turnAdmissionIdle, ""
	}
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	return q.state.phase, q.state.turnID
}

func (q turnAdmissionQueue) queuePending(
	ctx context.Context,
	msg llm.Message,
	phase turnAdmissionPhase,
	fallbackTurnID string,
) (TurnAdmissionResult, runtime.PendingInputStatus, bool) {
	if q.engine == nil {
		status := runtime.PendingInputStatus{TurnID: fallbackTurnID}
		return conflictResult("turn is not accepting pending input", runtime.ErrNoActiveTurn, status), status, false
	}
	status, err := q.engine.EnqueuePendingMessage(ctx, msg)
	if status.TurnID == "" {
		status.TurnID = fallbackTurnID
	}
	switch {
	case err == nil:
		return queuedResult(status), status, false
	case errors.Is(err, runtime.ErrPendingInputQueueFull):
		return rejectedResult(
			"pending_input_full",
			fmt.Sprintf("pending input queue full (%d/%d)", status.PendingCount, status.MaxPendingInputs),
			"wait for the active turn to drain pending input before sending more",
			true,
			err,
			status,
		), status, false
	case errors.Is(err, runtime.ErrNoActiveTurn):
		if phase == turnAdmissionRunning {
			q.complete(fallbackTurnID)
		}
		if phase == turnAdmissionIdle || phase == turnAdmissionRunning {
			return TurnAdmissionResult{}, status, true
		}
		return conflictResult("turn is not accepting pending input", err, status), status, false
	default:
		return errorResult(err, nil), status, false
	}
}

func (q turnAdmissionQueue) queuePersisted(ctx context.Context, record runtime.PendingInputRecord, phase turnAdmissionPhase, fallbackTurnID string) (TurnAdmissionResult, runtime.PendingInputStatus, bool) {
	status, err := q.engine.EnqueuePersistedPendingMessage(ctx, record)
	if status.TurnID == "" {
		status.TurnID = fallbackTurnID
	}
	switch {
	case err == nil:
		return queuedResult(status), status, false
	case errors.Is(err, runtime.ErrPendingInputQueueFull):
		return rejectedResult(
			"pending_input_full",
			fmt.Sprintf("pending input queue full (%d/%d)", status.PendingCount, status.MaxPendingInputs),
			"wait for the active turn to drain pending input before sending more",
			true,
			err,
			status,
		), status, false
	case errors.Is(err, runtime.ErrNoActiveTurn):
		if phase == turnAdmissionRunning {
			q.complete(fallbackTurnID)
		}
		if phase == turnAdmissionIdle || phase == turnAdmissionRunning {
			return TurnAdmissionResult{}, status, true
		}
		return conflictResult("turn is not accepting pending input", err, status), status, false
	default:
		return errorResult(err, nil), status, false
	}
}
