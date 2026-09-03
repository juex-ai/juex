package runtime

import (
	"fmt"

	"github.com/juex-ai/juex/internal/events"
)

type PendingInputRecovery struct {
	RecordID string
}

// RecoverPendingInputs reconciles the pending journal against committed
// admission events and the complete transcript, then returns opaque recovery
// handles in stable acceptance order. App executes them behind its startup
// barrier; ReceivePendingInput owns all state and retry classification.
func (e *Engine) RecoverPendingInputs() ([]PendingInputRecovery, error) {
	if e == nil {
		return nil, nil
	}
	queue := e.currentPendingInputQueue()
	threadState := e.currentThread()
	if queue == nil || threadState == nil {
		return nil, nil
	}

	admittedMessageIDs := map[string]struct{}{}
	threadState.ReplayEvents(func(event events.Event) {
		if event.Type == TurnAdmittedType {
			payload := payloadAs[TurnAdmittedPayload](event.Payload)
			if payload.MessageID != "" {
				admittedMessageIDs[payload.MessageID] = struct{}{}
			}
		}
	})
	records, err := queue.Records()
	if err != nil {
		return nil, fmt.Errorf("runtime: recover pending input journal: %w", err)
	}
	transcriptMessageIDs := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.MessageID != "" && threadState.HasMessageID(record.MessageID) {
			transcriptMessageIDs[record.MessageID] = struct{}{}
		}
	}
	if err := queue.ReconcileRecoveryFacts(PendingInputRecoveryFacts{
		AdmittedMessageIDs:   admittedMessageIDs,
		TranscriptMessageIDs: transcriptMessageIDs,
	}); err != nil {
		return nil, fmt.Errorf("runtime: reconcile pending input recovery facts: %w", err)
	}
	replayable, err := queue.Replayable("", 0)
	if err != nil {
		return nil, fmt.Errorf("runtime: list replayable pending input: %w", err)
	}
	result := make([]PendingInputRecovery, 0, len(replayable))
	for _, record := range replayable {
		result = append(result, PendingInputRecovery{RecordID: record.ID})
	}
	return result, nil
}
