package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

var errTurnAdmissionBusy = errors.New("app: Thread busy")

// turnAdmissionQueue keeps only App-owned command and compaction exclusion.
// Runtime is the sole authority for ordinary input start-versus-queue state.
type turnAdmissionQueue struct {
	state  *turnAdmission
	engine *runtime.Engine
}

func (a *App) admissionQueue() turnAdmissionQueue {
	if a == nil || a.Engine == nil {
		return turnAdmissionQueue{}
	}
	return turnAdmissionQueue{state: &a.turnAdmission, engine: a.Engine}
}

func (q turnAdmissionQueue) admitUser(ctx context.Context, message llm.Message) TurnAdmissionResult {
	if q.state == nil || q.engine == nil {
		return errorResult(fmt.Errorf("turn admission: app, engine, or Thread is not initialized"), nil)
	}
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()
	q.state.mu.Lock()
	phase := q.state.phase
	q.state.mu.Unlock()
	if phase == turnAdmissionCommand {
		return conflictResult("Thread busy", errTurnAdmissionBusy, q.engine.PendingInputStatus())
	}
	return admissionResultFromPendingInput(q.engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{Message: message}))
}

func admissionResultFromPendingInput(result runtime.PendingInputResult, err error) TurnAdmissionResult {
	if result.Retry == runtime.PendingInputRetryAfterTurn && errors.Is(err, runtime.ErrActiveTurnExists) {
		return conflictResult("Thread busy", err, result.Status)
	}
	switch result.Disposition {
	case runtime.PendingInputStarted:
		start := &AdmittedTurn{TurnID: result.TurnID, Message: result.Message}
		return TurnAdmissionResult{Kind: TurnAdmissionStarted, InputID: result.RecordID, TurnID: result.TurnID, Start: start}
	case runtime.PendingInputQueued:
		if errors.Is(err, runtime.ErrPendingInputQueueFull) {
			return rejectedResult(
				"pending_input_full",
				fmt.Sprintf("pending input queue full (%d/%d)", result.Status.PendingCount, result.Status.MaxPendingInputs),
				"wait for the active turn to drain pending input before sending more",
				true,
				err,
				result.Status,
			)
		}
		if err != nil {
			return errorResult(err, nil)
		}
		return queuedResult(result.RecordID, result.Status)
	default:
		if err != nil {
			return errorResult(err, nil)
		}
		return errorResult(fmt.Errorf("turn admission: unexpected pending input disposition %q", result.Disposition), nil)
	}
}

func (q turnAdmissionQueue) beginCompact() (string, error) {
	if q.state == nil || q.engine == nil {
		return "", runtime.ErrNoActiveTurn
	}
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	q.state.mu.Lock()
	busy := q.state.phase != turnAdmissionIdle
	q.state.mu.Unlock()
	if busy {
		return "", errTurnAdmissionBusy
	}
	turnID, err := q.engine.ReservePendingInputCompaction()
	if err != nil {
		return "", err
	}
	q.state.mu.Lock()
	q.state.phase = turnAdmissionCompacting
	q.state.turnID = turnID
	q.state.mu.Unlock()
	return turnID, nil
}

func (q turnAdmissionQueue) finishCompact(compactTurnID string) (*AdmittedTurn, error) {
	if q.state == nil || q.engine == nil {
		return nil, nil
	}
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	result, err := q.engine.FinishPendingInputCompaction(compactTurnID)
	q.state.mu.Lock()
	if q.state.phase == turnAdmissionCompacting && q.state.turnID == compactTurnID {
		q.state.phase = turnAdmissionIdle
		q.state.turnID = ""
	}
	q.state.mu.Unlock()
	if result.Disposition != runtime.PendingInputStarted {
		return nil, err
	}
	return &AdmittedTurn{TurnID: result.TurnID, Message: result.Message}, err
}

func (q turnAdmissionQueue) beginExclusiveCommand() bool {
	if q.state == nil || q.engine == nil {
		return false
	}
	q.state.transitionMu.Lock()
	defer q.state.transitionMu.Unlock()

	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	if q.state.phase != turnAdmissionIdle || q.engine.PendingInputLifecycleStatus().TurnID != "" {
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

func (q turnAdmissionQueue) snapshot() (turnAdmissionPhase, string) {
	if q.state == nil {
		return turnAdmissionIdle, ""
	}
	q.state.mu.Lock()
	defer q.state.mu.Unlock()
	return q.state.phase, q.state.turnID
}
