package runtime

import (
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/session"
)

// RecoverPendingInputRecords reconciles the pending journal against committed
// admission events and the complete transcript, then returns unexpired records
// in stable acceptance order. It does not reserve or run a Turn; App owns that
// process-level scheduling decision.
func (e *Engine) RecoverPendingInputRecords() ([]PendingInputRecord, error) {
	if e == nil {
		return nil, nil
	}
	queue := e.currentPendingInputQueue()
	sess := e.currentSession()
	if queue == nil || sess == nil {
		return nil, nil
	}

	admittedMessageIDs := map[string]struct{}{}
	if err := session.ReplayEvents(sess.Dir, func(event events.Event) {
		if event.Type == TurnAdmittedType {
			payload := payloadAs[TurnAdmittedPayload](event.Payload)
			if payload.MessageID != "" {
				admittedMessageIDs[payload.MessageID] = struct{}{}
			}
		}
	}); err != nil {
		return nil, fmt.Errorf("runtime: recover pending input admission facts: %w", err)
	}
	records, err := queue.Records()
	if err != nil {
		return nil, fmt.Errorf("runtime: recover pending input journal: %w", err)
	}
	transcriptMessageIDs := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.MessageID != "" && sess.HasMessageID(record.MessageID) {
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
	return replayable, nil
}
