package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

var errTurnAdmissionBusy = errors.New("app: session busy")

type turnAdmissionQueue struct {
	state  *turnAdmission
	engine *runtime.Engine
}

func (a *App) admissionQueue() turnAdmissionQueue {
	if a == nil {
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
	phase, activeTurnID := q.snapshot()
	if phase != turnAdmissionIdle {
		return q.queuePending(ctx, msg, activeTurnID)
	}

	turnID := ids.NextTurnID("turn")
	q.state.mu.Lock()
	if q.state.phase != turnAdmissionIdle {
		activeTurnID = q.state.turnID
		q.state.mu.Unlock()
		return q.queuePending(ctx, msg, activeTurnID)
	}
	if err := q.engine.ReserveTurnID(turnID); err != nil {
		q.state.mu.Unlock()
		return conflictResult(err.Error(), err, runtime.PendingInputStatus{})
	}
	q.state.phase = turnAdmissionRunning
	q.state.turnID = turnID
	q.state.mu.Unlock()

	return TurnAdmissionResult{
		Kind:   TurnAdmissionStarted,
		TurnID: turnID,
		Start:  &AdmittedTurn{TurnID: turnID, Message: msg},
	}
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
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if q.state.phase != turnAdmissionIdle {
		return errTurnAdmissionBusy
	}
	if err := q.engine.ReserveTurnID(turnID); err != nil {
		return err
	}
	q.state.phase = turnAdmissionCompacting
	q.state.turnID = turnID
	return nil
}

func (q turnAdmissionQueue) finishCompact(compactTurnID string, ids TurnIDAllocator) *AdmittedTurn {
	if q.state == nil || q.engine == nil || ids == nil {
		return nil
	}
	nextTurnID := ids.NextTurnID("turn")
	msg, _, promoted := q.engine.PromotePendingInputTurn(compactTurnID, nextTurnID)
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if promoted {
		q.state.phase = turnAdmissionRunning
		q.state.turnID = nextTurnID
		return &AdmittedTurn{TurnID: nextTurnID, Message: msg}
	}
	if q.state.phase == turnAdmissionCompacting && q.state.turnID == compactTurnID {
		q.state.phase = turnAdmissionIdle
		q.state.turnID = ""
	}
	return nil
}

func (q turnAdmissionQueue) beginExclusiveCommand() bool {
	if q.state == nil {
		return false
	}
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

func (q turnAdmissionQueue) queuePending(ctx context.Context, msg llm.Message, fallbackTurnID string) TurnAdmissionResult {
	if q.engine == nil {
		return conflictResult("turn is not accepting pending input", runtime.ErrNoActiveTurn, runtime.PendingInputStatus{TurnID: fallbackTurnID})
	}
	status, err := q.engine.EnqueuePendingMessage(ctx, msg)
	if status.TurnID == "" {
		status.TurnID = fallbackTurnID
	}
	switch {
	case err == nil:
		return queuedResult(status)
	case errors.Is(err, runtime.ErrPendingInputQueueFull):
		return rejectedResult(
			"pending_input_full",
			fmt.Sprintf("pending input queue full (%d/%d)", status.PendingCount, status.MaxPendingInputs),
			"wait for the active turn to drain pending input before sending more",
			true,
			err,
			status,
		)
	case errors.Is(err, runtime.ErrNoActiveTurn):
		return conflictResult("turn is not accepting pending input", err, status)
	default:
		return errorResult(err, nil)
	}
}
