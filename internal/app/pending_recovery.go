package app

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		a.handoffPersistedInputAfterRecovery(record)
		return externalInputDelivery{Record: record, Queued: true}, err
	}
	delivery, err := a.resumePersistedInputLocked(ctx, record)
	if shouldRetryPersistedInputHandoff(delivery, err) {
		a.handoffPersistedInputAfterRecovery(record)
	}
	return delivery, err
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

func (a *App) startPendingInputRecovery(records []runtime.PendingInputRecord) {
	if a == nil || a.Engine == nil || len(records) == 0 {
		return
	}
	records = append([]runtime.PendingInputRecord(nil), records...)
	done := make(chan struct{})
	a.pendingRecoveryDone = done
	a.pendingRecovery.Add(1)
	go func() {
		defer a.pendingRecovery.Done()
		defer close(done)
		for _, record := range records {
			if a.ctx.Err() != nil {
				return
			}
			if record.ID == "" {
				continue
			}
			a.sessionMu.RLock()
			delivery, err := a.resumePersistedInputLocked(a.ctx, record)
			a.sessionMu.RUnlock()
			handedOff := false
			if shouldRetryPersistedInputHandoff(delivery, err) {
				handedOff = a.handoffPersistedInputAfterRecovery(record)
			}
			inert := pendingRecoveryRecordInert(delivery.Record)
			if err != nil && !handedOff && !inert && a.ctx.Err() == nil && !errors.Is(err, context.Canceled) && a.stderr != nil {
				fmt.Fprintf(a.stderr, "juex: warning: resume pending input %q: %v\n", record.ID, err)
			}
			if handedOff || !inert {
				return
			}
		}
	}()
}

func pendingRecoveryRecordInert(record runtime.PendingInputRecord) bool {
	switch record.State {
	case runtime.PendingInputStateExpired, runtime.PendingInputStateDropped, runtime.PendingInputStateProcessed:
		return true
	default:
		return false
	}
}

// activateExternalInputAfterPendingRecovery publishes and starts the recovery
// barrier before a startup notification gate can expose external producers.
func (a *App) activateExternalInputAfterPendingRecovery(
	gate *mcpNotificationGate,
	records []runtime.PendingInputRecord,
	activateProducers func(),
) {
	if len(records) > 0 {
		a.startPendingInputRecovery(records)
	}
	if activateProducers != nil {
		activateProducers()
	}
	if gate != nil {
		gate.Activate()
	}
}

func (a *App) waitPendingInputRecoveryContext(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var appDone <-chan struct{}
	if a.ctx != nil {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		appDone = a.ctx.Done()
	}
	if a.pendingRecoveryDone == nil {
		return nil
	}
	select {
	case <-a.pendingRecoveryDone:
		// BeginClose and recovery completion may become ready together. Check
		// both lifetimes again so shutdown always wins that race.
		if a.ctx != nil {
			if err := a.ctx.Err(); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-appDone:
		return a.ctx.Err()
	}
}

// handoffPersistedInputAfterRecovery transfers caller-canceled delivery to
// App-owned work. The durable record remains available for restart if the App
// is already closing, and duplicate callers share one handoff by record ID.
func (a *App) handoffPersistedInputAfterRecovery(record runtime.PendingInputRecord) bool {
	if a == nil || a.Engine == nil || record.ID == "" || a.ctx == nil {
		return false
	}
	a.pendingHandoffMu.Lock()
	if a.pendingHandoffClosed || a.ctx.Err() != nil {
		a.pendingHandoffMu.Unlock()
		return false
	}
	if _, ok := a.pendingHandoffIDs[record.ID]; ok {
		a.pendingHandoffMu.Unlock()
		return true
	}
	if a.pendingHandoffIDs == nil {
		a.pendingHandoffIDs = make(map[string]struct{})
	}
	a.pendingHandoffIDs[record.ID] = struct{}{}
	a.pendingHandoffs.Add(1)
	a.pendingHandoffMu.Unlock()

	go func() {
		defer func() {
			a.pendingHandoffMu.Lock()
			delete(a.pendingHandoffIDs, record.ID)
			a.pendingHandoffMu.Unlock()
			a.pendingHandoffs.Done()
		}()
		delay := 25 * time.Millisecond
		for {
			if err := a.waitPendingInputRecoveryContext(a.ctx); err != nil {
				return
			}
			a.sessionMu.RLock()
			delivery, err := a.resumePersistedInputLocked(a.ctx, record)
			a.sessionMu.RUnlock()
			if !shouldRetryPersistedInputHandoff(delivery, err) {
				if err != nil && a.ctx.Err() == nil && !errors.Is(err, context.Canceled) && a.stderr != nil {
					fmt.Fprintf(a.stderr, "juex: warning: resume handed-off pending input %q: %v\n", record.ID, err)
				}
				return
			}
			timer := time.NewTimer(delay)
			select {
			case <-a.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		}
	}()
	return true
}

func shouldRetryPersistedInputHandoff(delivery externalInputDelivery, err error) bool {
	return delivery.Queued && err != nil
}

func (a *App) waitPendingInputRecovery() error {
	if a == nil {
		return nil
	}
	a.pendingRecovery.Wait()
	return nil
}

func (a *App) closeAndWaitPendingInputWork() error {
	if a == nil {
		return nil
	}
	a.pendingHandoffMu.Lock()
	a.pendingHandoffClosed = true
	a.pendingHandoffMu.Unlock()
	a.pendingRecovery.Wait()
	a.pendingHandoffs.Wait()
	return nil
}
