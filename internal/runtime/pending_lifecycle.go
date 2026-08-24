package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
)

type PendingInputDisposition string

const (
	PendingInputStarted   PendingInputDisposition = "started"
	PendingInputQueued    PendingInputDisposition = "queued"
	PendingInputProcessed PendingInputDisposition = "processed"
	PendingInputExpired   PendingInputDisposition = "expired"
	PendingInputDropped   PendingInputDisposition = "dropped"
)

type PendingInputRetry string

const (
	PendingInputNoRetry            PendingInputRetry = ""
	PendingInputRetryAfterTurn     PendingInputRetry = "after_turn"
	PendingInputRetryAfterRecovery PendingInputRetry = "after_recovery"
	PendingInputRetryAfterStorage  PendingInputRetry = "after_storage"
	PendingInputRetryAdmission     PendingInputRetry = "admission"
)

// PendingInputRequest is the source-neutral Framework request for one input.
// Options requests durable acceptance with a stable source identity and TTL;
// RecordID resumes an already accepted record without exposing its state.
type PendingInputRequest struct {
	Message  llm.Message
	Options  *PendingInputOptions
	RecordID string
	// DeferDelivery durably accepts a new external input without publishing it
	// to the live queue. App uses it before the startup recovery barrier.
	DeferDelivery bool
}

// PendingInputResult tells an Adapter what the Framework accepted and whether
// it should start a Turn, leave the input queued, or stop retrying an inert
// record. State transitions remain private to the runtime Module.
type PendingInputResult struct {
	Disposition PendingInputDisposition
	Retry       PendingInputRetry
	RecordID    string
	TurnID      string
	Message     llm.Message
	Status      PendingInputStatus
}

// ReceivePendingInput owns the complete start-or-queue decision for direct and
// external input. Calls are serialized so App does not need a mirrored running
// state or a transport-owned Turn identity.
func (e *Engine) ReceivePendingInput(ctx context.Context, request PendingInputRequest) (PendingInputResult, error) {
	if e == nil {
		return PendingInputResult{}, ErrNoActiveTurn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PendingInputResult{}, err
	}

	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()

	if request.RecordID != "" {
		return e.receivePersistedPendingInput(ctx, request.RecordID)
	}
	if request.Options != nil {
		record, err := e.PersistPendingMessageWithOptions(ctx, request.Message, *request.Options)
		if err != nil {
			return PendingInputResult{Retry: PendingInputRetryAfterStorage}, err
		}
		if request.DeferDelivery {
			return PendingInputResult{
				Disposition: PendingInputQueued,
				Retry:       PendingInputRetryAfterRecovery,
				RecordID:    record.ID,
				Status:      e.PendingInputStatus(),
			}, nil
		}
		return e.receivePersistedPendingInput(ctx, record.ID)
	}
	return e.receiveNewPendingInput(ctx, request.Message)
}

// ResolvePendingInput classifies the authoritative durable result after an
// Adapter executes a started Turn. A processed record is delivered even when
// the Provider failed later; replayable records carry the runtime's handoff
// instruction instead of requiring App to infer it from raw state.
func (e *Engine) ResolvePendingInput(recordID string, cause error) (PendingInputResult, error) {
	record, ok, err := e.PersistedPendingMessage(recordID)
	if err != nil {
		return PendingInputResult{}, errors.Join(cause, err)
	}
	if !ok {
		return PendingInputResult{}, errors.Join(cause, fmt.Errorf("runtime: persisted input %q not found", recordID))
	}
	result := PendingInputResult{RecordID: record.ID, Status: e.PendingInputStatus()}
	switch record.State {
	case PendingInputStateProcessed:
		result.Disposition = PendingInputProcessed
		return result, cause
	case PendingInputStatePending, PendingInputStateAdmitted:
		result.Disposition = PendingInputQueued
		if cause != nil {
			result.Retry = PendingInputRetryAfterTurn
		}
		return result, cause
	case PendingInputStateExpired:
		result.Disposition = PendingInputExpired
		return result, errors.Join(cause, ErrPendingInputExpired)
	case PendingInputStateDropped, PendingInputStateAccepting:
		result.Disposition = PendingInputDropped
		return result, errors.Join(cause, ErrPendingInputHandled)
	default:
		return result, errors.Join(cause, fmt.Errorf("runtime: persisted input %q has unknown state %q", recordID, record.State))
	}
}

// DiscardPendingInput makes an accepted record permanently inert. Source
// Adapters identify when their own ownership became stale; the runtime owns the
// durable transition and tells App whether storage retry is required.
func (e *Engine) DiscardPendingInput(recordID string) (PendingInputResult, error) {
	if e == nil || recordID == "" {
		return PendingInputResult{}, nil
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	if err := e.DropPersistedPendingMessage(recordID); err != nil {
		return PendingInputResult{Retry: PendingInputRetryAfterStorage}, err
	}
	record, ok, err := e.PersistedPendingMessage(recordID)
	if err != nil {
		return PendingInputResult{Retry: PendingInputRetryAfterStorage}, err
	}
	if !ok {
		return PendingInputResult{}, nil
	}
	result := PendingInputResult{RecordID: record.ID, Status: e.PendingInputStatus()}
	switch record.State {
	case PendingInputStateProcessed:
		result.Disposition = PendingInputProcessed
	case PendingInputStateExpired:
		result.Disposition = PendingInputExpired
	default:
		result.Disposition = PendingInputDropped
	}
	return result, nil
}

// ReservePendingInputCompaction establishes the exclusive runtime Turn used by
// manual compaction and returns its Framework-owned identity.
func (e *Engine) ReservePendingInputCompaction() (string, error) {
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	turnID := pendingInputTurnID("compact")
	if err := e.ReserveCompactionTurnID(turnID); err != nil {
		return "", err
	}
	return turnID, nil
}

// FinishPendingInputCompaction releases a completed compaction and promotes
// the oldest queued input with a new Framework-owned Turn identity.
func (e *Engine) FinishPendingInputCompaction(compactTurnID string) (PendingInputResult, error) {
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	nextTurnID := pendingInputTurnID("turn")
	message, status, promoted, err := e.PromotePendingInputTurn(compactTurnID, nextTurnID)
	if err != nil {
		return PendingInputResult{Status: status}, err
	}
	if !promoted {
		return PendingInputResult{Status: status}, nil
	}
	recordID, recordErr := e.pendingInputRecordIDByMessageID(message.ID)
	if recordErr != nil {
		return PendingInputResult{}, recordErr
	}
	return PendingInputResult{
		Disposition: PendingInputStarted,
		RecordID:    recordID,
		TurnID:      nextTurnID,
		Message:     message,
		Status:      status,
	}, nil
}

func (e *Engine) receiveNewPendingInput(ctx context.Context, message llm.Message) (PendingInputResult, error) {
	message = llm.ClassifyUserMessage(message)
	for attempt := 0; attempt < 2; attempt++ {
		status := e.PendingInputStatus()
		if status.TurnID != "" {
			queued, record, err := e.enqueuePendingMessageWithOptions(ctx, message, PendingInputOptions{})
			if errors.Is(err, ErrNoActiveTurn) {
				continue
			}
			result := PendingInputResult{Disposition: PendingInputQueued, RecordID: record.ID, Status: queued}
			if errors.Is(err, ErrPendingInputQueueFull) {
				result.Retry = PendingInputRetryAfterTurn
			}
			return result, err
		}

		turnID := pendingInputTurnID("turn")
		accepted, err := e.AdmitTurnMessage(turnID, message)
		if errors.Is(err, ErrActiveTurnExists) {
			continue
		}
		if err != nil {
			return PendingInputResult{}, err
		}
		recordID, err := e.pendingInputRecordIDByMessageID(accepted.ID)
		if err != nil {
			return PendingInputResult{}, err
		}
		return PendingInputResult{
			Disposition: PendingInputStarted,
			RecordID:    recordID,
			TurnID:      turnID,
			Message:     accepted,
			Status:      e.PendingInputStatus(),
		}, nil
	}
	return PendingInputResult{Status: e.PendingInputStatus()}, fmt.Errorf("runtime: active turn changed while receiving input")
}

func (e *Engine) receivePersistedPendingInput(ctx context.Context, recordID string) (PendingInputResult, error) {
	record, ok, err := e.PersistedPendingMessage(recordID)
	if err != nil {
		return PendingInputResult{}, err
	}
	if !ok {
		return PendingInputResult{}, fmt.Errorf("runtime: persisted input %q not found", recordID)
	}
	switch record.State {
	case PendingInputStateProcessed:
		return PendingInputResult{Disposition: PendingInputProcessed, RecordID: record.ID}, nil
	case PendingInputStateExpired:
		return PendingInputResult{Disposition: PendingInputExpired, RecordID: record.ID}, ErrPendingInputExpired
	case PendingInputStateDropped, PendingInputStateAccepting:
		return PendingInputResult{Disposition: PendingInputDropped, RecordID: record.ID}, ErrPendingInputHandled
	}
	status := e.PendingInputStatus()
	if record.State == PendingInputStateAdmitted && record.TurnID != "" && record.TurnID == status.TurnID {
		return PendingInputResult{Disposition: PendingInputQueued, RecordID: record.ID, Status: status}, nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		status, enqueueErr := e.EnqueuePersistedPendingMessage(ctx, record)
		switch {
		case enqueueErr == nil:
			current, _, stateErr := e.PersistedPendingMessage(record.ID)
			if stateErr != nil {
				return PendingInputResult{}, stateErr
			}
			return PendingInputResult{Disposition: PendingInputQueued, RecordID: current.ID, Status: status}, nil
		case errors.Is(enqueueErr, ErrPendingInputQueueFull):
			current, _, stateErr := e.PersistedPendingMessage(record.ID)
			if stateErr != nil {
				return PendingInputResult{}, errors.Join(enqueueErr, stateErr)
			}
			return PendingInputResult{Disposition: PendingInputQueued, Retry: PendingInputRetryAfterTurn, RecordID: current.ID, Status: status}, enqueueErr
		case errors.Is(enqueueErr, ErrPendingInputExpired):
			current, _, stateErr := e.PersistedPendingMessage(record.ID)
			if stateErr != nil {
				return PendingInputResult{}, errors.Join(enqueueErr, stateErr)
			}
			return PendingInputResult{Disposition: PendingInputExpired, RecordID: current.ID, Status: status}, enqueueErr
		case errors.Is(enqueueErr, ErrPendingInputHandled):
			current, _, stateErr := e.PersistedPendingMessage(record.ID)
			if stateErr != nil {
				return PendingInputResult{}, errors.Join(enqueueErr, stateErr)
			}
			disposition := PendingInputDropped
			if current.State == PendingInputStateProcessed {
				disposition = PendingInputProcessed
			}
			return PendingInputResult{Disposition: disposition, RecordID: current.ID, Status: status}, enqueueErr
		case !errors.Is(enqueueErr, ErrNoActiveTurn):
			return PendingInputResult{RecordID: record.ID, Status: status}, enqueueErr
		}

		turnID := pendingInputTurnID("turn")
		accepted, admitErr := e.AdmitTurnMessage(turnID, record.Message)
		if errors.Is(admitErr, ErrActiveTurnExists) {
			continue
		}
		if admitErr != nil {
			current, _, stateErr := e.PersistedPendingMessage(record.ID)
			return PendingInputResult{Disposition: PendingInputQueued, Retry: PendingInputRetryAdmission, RecordID: current.ID}, errors.Join(admitErr, stateErr)
		}
		current, _, stateErr := e.PersistedPendingMessage(record.ID)
		if stateErr != nil {
			return PendingInputResult{}, stateErr
		}
		return PendingInputResult{
			Disposition: PendingInputStarted,
			RecordID:    current.ID,
			TurnID:      turnID,
			Message:     accepted,
			Status:      e.PendingInputStatus(),
		}, nil
	}
	return PendingInputResult{Disposition: PendingInputQueued, Retry: PendingInputRetryAfterTurn, RecordID: record.ID, Status: e.PendingInputStatus()}, fmt.Errorf("runtime: active turn changed while receiving persisted input")
}

func (e *Engine) pendingInputRecordIDByMessageID(messageID string) (string, error) {
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return "", fmt.Errorf("runtime: pending input queue unavailable")
	}
	records, err := queue.Records()
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.MessageID == messageID {
			return record.ID, nil
		}
	}
	return "", fmt.Errorf("runtime: pending input for message %q not found", messageID)
}

func pendingInputTurnID(prefix string) string {
	return prefix + "-" + newID()
}
