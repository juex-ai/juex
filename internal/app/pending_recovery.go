package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

type externalInputDelivery struct {
	RecordID  string
	Queued    bool
	Delivered bool
	Retry     runtime.PendingInputRetry
}

const maxExternalInputStorageAttempts = 8

type externalInputSessionLease struct {
	app  *App
	refs atomic.Int32
}

func (a *App) acquireExternalInputSessionLease() *externalInputSessionLease {
	a.sessionHandoffMu.RLock()
	lease := &externalInputSessionLease{app: a}
	lease.refs.Store(1)
	return lease
}

func (l *externalInputSessionLease) Retain() {
	if l != nil {
		l.refs.Add(1)
	}
}

func (l *externalInputSessionLease) Release() {
	if l != nil && l.refs.Add(-1) == 0 {
		l.app.sessionHandoffMu.RUnlock()
	}
}

// deliverExternalInputLocked durably accepts transport input before asking the
// runtime lifecycle whether to attach it or start an idle Turn.
// The caller holds sessionMu.RLock for the complete attached-session lifetime.
func (a *App) deliverExternalInputLocked(
	ctx context.Context,
	message llm.Message,
	opts runtime.PendingInputOptions,
	sessionLease *externalInputSessionLease,
	handoff bool,
	valid func() error,
) (externalInputDelivery, error) {
	accepted, err := a.Engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{
		Message:       message,
		Options:       &opts,
		DeferDelivery: true,
	})
	if err != nil {
		return externalInputDeliveryFromRuntime(accepted), err
	}
	recordID := accepted.RecordID
	if valid != nil {
		if err := valid(); err != nil {
			return externalInputDelivery{RecordID: recordID}, errors.Join(err, a.discardExternalInput(recordID))
		}
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		if handoff {
			a.handoffPersistedInputAfterRecovery(recordID, sessionLease)
		}
		return externalInputDelivery{RecordID: recordID, Queued: true, Retry: runtime.PendingInputRetryAfterRecovery}, err
	}
	delivery, err := a.resumePersistedInputLocked(ctx, recordID)
	if handoff && shouldRetryPersistedInputHandoff(delivery, err) {
		a.handoffPersistedInputAfterRecovery(recordID, sessionLease)
	}
	return delivery, err
}

// resumePersistedInputLocked follows the Framework-owned lifecycle outcome;
// App owns only the Session lease and execution of a returned start action.
func (a *App) resumePersistedInputLocked(ctx context.Context, recordID string) (externalInputDelivery, error) {
	result, receiveErr := a.Engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{RecordID: recordID})
	if result.Disposition != runtime.PendingInputStarted {
		return externalInputDeliveryFromRuntime(result), receiveErr
	}
	_, runErr := a.Engine.TurnMessageWithID(ctx, result.Message, result.TurnID)
	resolved, err := a.Engine.ResolvePendingInput(recordID, runErr)
	return externalInputDeliveryFromRuntime(resolved), err
}

func externalInputDeliveryFromRuntime(result runtime.PendingInputResult) externalInputDelivery {
	return externalInputDelivery{
		RecordID:  result.RecordID,
		Queued:    result.Disposition == runtime.PendingInputQueued,
		Delivered: result.Disposition == runtime.PendingInputProcessed,
		Retry:     result.Retry,
	}
}

// deliverExternalInputUntilSettled is the shared App delivery Adapter for
// sources that must retain their own validity check while following runtime
// retry instructions. It never interprets durable Pending states.
func (a *App) deliverExternalInputUntilSettled(
	ctx context.Context,
	message llm.Message,
	opts runtime.PendingInputOptions,
	valid func() error,
) (externalInputDelivery, error) {
	lease := a.acquireExternalInputSessionLease()
	defer lease.Release()
	backoff := 50 * time.Millisecond
	storageAttempts := 0
	admissionAttempts := 0
	var last externalInputDelivery
	for {
		if err := ctx.Err(); err != nil {
			return last, errors.Join(err, a.discardExternalInput(last.RecordID))
		}
		a.sessionMu.RLock()
		delivery, err := a.deliverExternalInputLocked(ctx, message, opts, lease, false, valid)
		a.sessionMu.RUnlock()
		last = delivery
		if err == nil || delivery.Retry == runtime.PendingInputNoRetry {
			return delivery, err
		}
		if delivery.Retry == runtime.PendingInputRetryAfterStorage {
			storageAttempts++
			if storageAttempts >= maxExternalInputStorageAttempts {
				return delivery, err
			}
		}
		if delivery.Retry == runtime.PendingInputRetryAdmission {
			admissionAttempts++
			if admissionAttempts >= maxExternalInputStorageAttempts {
				return delivery, err
			}
		}
		delay := 100 * time.Millisecond
		if delivery.Retry == runtime.PendingInputRetryAfterStorage || delivery.Retry == runtime.PendingInputRetryAdmission {
			delay = backoff
		}
		if delivery.Retry == runtime.PendingInputRetryAfterRecovery {
			delay = 25 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return delivery, errors.Join(ctx.Err(), a.discardExternalInput(delivery.RecordID))
		case <-timer.C:
		}
		if delivery.Retry == runtime.PendingInputRetryAfterStorage || delivery.Retry == runtime.PendingInputRetryAdmission {
			if backoff < time.Second {
				backoff *= 2
				if backoff > time.Second {
					backoff = time.Second
				}
			}
		}
	}
}

func (a *App) discardExternalInput(recordID string) error {
	if a == nil || a.Engine == nil || recordID == "" {
		return nil
	}
	delay := 50 * time.Millisecond
	var last error
	for attempt := 0; attempt < maxExternalInputStorageAttempts; attempt++ {
		result, err := a.Engine.DiscardPendingInput(recordID)
		if err == nil || result.Retry == runtime.PendingInputNoRetry {
			return err
		}
		last = err
		if attempt+1 < maxExternalInputStorageAttempts {
			time.Sleep(delay)
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		}
	}
	return last
}

func (a *App) startPendingInputRecovery(records []runtime.PendingInputRecovery) {
	if a == nil || a.Engine == nil || len(records) == 0 {
		return
	}
	records = append([]runtime.PendingInputRecovery(nil), records...)
	done := make(chan struct{})
	a.pendingRecoveryDone = done
	a.pendingRecovery.Add(1)
	go func() {
		defer a.pendingRecovery.Done()
		defer close(done)
		for _, recovery := range records {
			if recovery.RecordID == "" {
				continue
			}
			delivery, err := a.resumePersistedInputDuringRecovery(recovery.RecordID)
			inert := delivery.Retry == runtime.PendingInputNoRetry
			if err != nil && !inert && a.ctx.Err() == nil && !errors.Is(err, context.Canceled) && a.stderr != nil {
				fmt.Fprintf(a.stderr, "juex: warning: resume pending input %q: %v\n", recovery.RecordID, err)
			}
			if !inert {
				return
			}
		}
	}()
}

// resumePersistedInputDuringRecovery keeps the startup barrier and its Session
// ownership until a replayable admission either attaches or becomes inert.
// A generic handoff cannot own this retry because handoffs wait for the startup
// barrier and would release newer inputs before they finish.
func (a *App) resumePersistedInputDuringRecovery(recordID string) (externalInputDelivery, error) {
	delay := 25 * time.Millisecond
	for {
		if err := a.ctx.Err(); err != nil {
			return externalInputDelivery{}, err
		}
		a.sessionMu.RLock()
		delivery, err := a.resumePersistedInputLocked(a.ctx, recordID)
		a.sessionMu.RUnlock()
		if !shouldRetryPersistedInputHandoff(delivery, err) {
			return delivery, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return delivery, a.ctx.Err()
		case <-timer.C:
		}
		if delay < time.Second {
			delay *= 2
			if delay > time.Second {
				delay = time.Second
			}
		}
	}
}

// activateExternalInputAfterPendingRecovery publishes and starts the recovery
// barrier before a startup notification gate can expose external producers.
func (a *App) activateExternalInputAfterPendingRecovery(
	gate *mcpNotificationGate,
	records []runtime.PendingInputRecovery,
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
func (a *App) handoffPersistedInputAfterRecovery(
	recordID string,
	sessionLease *externalInputSessionLease,
) bool {
	if a == nil || a.Engine == nil || recordID == "" || a.ctx == nil {
		return false
	}
	a.pendingHandoffMu.Lock()
	if a.pendingHandoffClosed || a.ctx.Err() != nil {
		a.pendingHandoffMu.Unlock()
		return false
	}
	if _, ok := a.pendingHandoffIDs[recordID]; ok {
		a.pendingHandoffMu.Unlock()
		return true
	}
	if a.pendingHandoffIDs == nil {
		a.pendingHandoffIDs = make(map[string]struct{})
	}
	a.pendingHandoffIDs[recordID] = struct{}{}
	a.pendingHandoffs.Add(1)
	sessionLease.Retain()
	a.pendingHandoffMu.Unlock()

	go func() {
		defer func() {
			a.pendingHandoffMu.Lock()
			delete(a.pendingHandoffIDs, recordID)
			a.pendingHandoffMu.Unlock()
			a.pendingHandoffs.Done()
			sessionLease.Release()
		}()
		delay := 25 * time.Millisecond
		for {
			if err := a.waitPendingInputRecoveryContext(a.ctx); err != nil {
				return
			}
			a.sessionMu.RLock()
			delivery, err := a.resumePersistedInputLocked(a.ctx, recordID)
			a.sessionMu.RUnlock()
			if !shouldRetryPersistedInputHandoff(delivery, err) {
				if err != nil && a.ctx.Err() == nil && !errors.Is(err, context.Canceled) && a.stderr != nil {
					fmt.Fprintf(a.stderr, "juex: warning: resume handed-off pending input %q: %v\n", recordID, err)
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
	return delivery.Retry != runtime.PendingInputNoRetry && err != nil
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
