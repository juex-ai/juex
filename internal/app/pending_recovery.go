package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

type externalInputDelivery struct {
	Record    runtime.PendingInputRecord
	Queued    bool
	Delivered bool
}

// deliverExternalInputLocked persists transport input before asking the
// process-level admission queue whether to attach it or start an idle Turn.
// The caller holds sessionMu.RLock for the complete attached-session lifetime.
func (a *App) deliverExternalInputLocked(ctx context.Context, message llm.Message, opts runtime.PendingInputOptions) (externalInputDelivery, error) {
	record, err := a.Engine.PersistPendingMessageWithOptions(ctx, message, opts)
	if err != nil {
		return externalInputDelivery{}, err
	}
	return a.resumePersistedInputLocked(ctx, record)
}

// resumePersistedInputLocked classifies delivery from the Framework-owned
// durable state even when admission or Turn execution also returns an error.
func (a *App) resumePersistedInputLocked(ctx context.Context, record runtime.PendingInputRecord) (externalInputDelivery, error) {
	current, ok, err := a.Engine.PersistedPendingMessage(record.ID)
	if err != nil {
		return externalInputDelivery{Record: record}, err
	}
	if ok {
		record = current
	}
	if record.State == runtime.PendingInputStateProcessed {
		return externalInputDelivery{Record: record, Delivered: true}, nil
	}
	if record.State != runtime.PendingInputStatePending && record.State != runtime.PendingInputStateAdmitted {
		return externalInputDelivery{Record: record}, externalInputStateError(record)
	}

	result := a.admitPersistedUserTurn(ctx, record, TurnIDFunc(nextExternalTurnID))
	var runErr error
	if result.Start != nil {
		_, runErr = a.Engine.TurnMessageWithID(ctx, result.Start.Message, result.Start.TurnID)
		a.CompleteAdmittedTurn(result.Start.TurnID)
	}
	cause := errors.Join(result.Err, runErr)
	current, ok, stateErr := a.Engine.PersistedPendingMessage(record.ID)
	if stateErr != nil {
		return externalInputDelivery{Record: record}, errors.Join(cause, stateErr)
	}
	if !ok {
		return externalInputDelivery{Record: record}, errors.Join(cause, fmt.Errorf("app: persisted input %q disappeared", record.ID))
	}
	delivery, handled := classifyExternalInputRecord(current)
	if handled {
		return delivery, cause
	}
	return delivery, errors.Join(cause, externalInputStateError(current))
}

func classifyExternalInputRecord(record runtime.PendingInputRecord) (externalInputDelivery, bool) {
	delivery := externalInputDelivery{Record: record}
	switch record.State {
	case runtime.PendingInputStatePending, runtime.PendingInputStateAdmitted:
		delivery.Queued = true
		return delivery, true
	case runtime.PendingInputStateProcessed:
		delivery.Delivered = true
		return delivery, true
	case runtime.PendingInputStateExpired:
		return delivery, false
	case runtime.PendingInputStateDropped, runtime.PendingInputStateAccepting:
		return delivery, false
	default:
		return delivery, false
	}
}

func externalInputStateError(record runtime.PendingInputRecord) error {
	switch record.State {
	case runtime.PendingInputStateExpired:
		return fmt.Errorf("app: persisted input %q: %w", record.ID, runtime.ErrPendingInputExpired)
	case runtime.PendingInputStateDropped:
		return fmt.Errorf("app: persisted input %q: %w", record.ID, runtime.ErrPendingInputHandled)
	default:
		return fmt.Errorf("app: persisted input %q is not replayable in state %q", record.ID, record.State)
	}
}

func nextExternalTurnID(prefix string) string {
	event := events.Normalize(events.Event{Type: runtime.TurnAdmittedType})
	return prefix + "-" + event.ID
}

func (a *App) startPendingInputRecovery(record runtime.PendingInputRecord) {
	if a == nil || a.Engine == nil || record.ID == "" {
		return
	}
	a.pendingRecovery.Add(1)
	go func() {
		defer a.pendingRecovery.Done()
		a.sessionMu.RLock()
		_, err := a.resumePersistedInputLocked(a.ctx, record)
		a.sessionMu.RUnlock()
		if err != nil && !errors.Is(err, context.Canceled) && a.stderr != nil {
			fmt.Fprintf(a.stderr, "juex: warning: resume pending input %q: %v\n", record.ID, err)
		}
	}()
}

func (a *App) waitPendingInputRecovery() error {
	if a == nil {
		return nil
	}
	a.pendingRecovery.Wait()
	return nil
}
