package session

import (
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/toolevents"
)

// toolCallRecoveryPhase is the Session recovery interpretation of durable Tool
// facts for one call. Runtime owns live execution and only commits those facts.
type toolCallRecoveryPhase string

const (
	toolCallRecoveryUncheckpointed  toolCallRecoveryPhase = "uncheckpointed"
	toolCallRecoveryDeclared        toolCallRecoveryPhase = "declared"
	toolCallRecoveryStarted         toolCallRecoveryPhase = "started"
	toolCallRecoveryOutcomeRecorded toolCallRecoveryPhase = "outcome-recorded"
)

type toolCallRecoveryState struct {
	turnID    string
	iter      int
	callIndex int
	messageID string
	toolUseID string
	name      string
	input     map[string]any
	phase     toolCallRecoveryPhase
	outcome   *toolevents.RecordedOutcome
}

type toolCallRecoveryProjection map[string]toolCallRecoveryState

func (projection toolCallRecoveryProjection) lookup(messageID, toolUseID string) toolCallRecoveryState {
	if projection == nil {
		return toolCallRecoveryState{messageID: messageID, toolUseID: toolUseID, phase: toolCallRecoveryUncheckpointed}
	}
	if state, ok := projection[toolCallRecoveryKey(messageID, toolUseID)]; ok {
		return state
	}
	return toolCallRecoveryState{messageID: messageID, toolUseID: toolUseID, phase: toolCallRecoveryUncheckpointed}
}

// projectToolCallRecovery folds the Session journal into per-call repair state.
// It is not a live Tool executor or a batch scheduler.
func projectToolCallRecovery(journal []events.Event) (toolCallRecoveryProjection, error) {
	projection := make(toolCallRecoveryProjection)
	for _, event := range journal {
		switch event.Type {
		case "llm.responded":
			var payload struct {
				ToolCalls []toolevents.ToolCallPayload `json:"tool_calls"`
			}
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode llm.responded recovery payload: %w", err)
			}
			for _, call := range payload.ToolCalls {
				projection[toolCallRecoveryKey(call.MessageID, call.ToolUseID)] = toolCallRecoveryState{
					turnID: event.TurnID, iter: call.Iter, callIndex: call.CallIndex,
					messageID: call.MessageID, toolUseID: call.ToolUseID, name: call.Name, input: call.Input,
					phase: toolCallRecoveryDeclared,
				}
			}
		case toolevents.RequestedType:
			var payload toolevents.RequestedPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.requested recovery payload: %w", err)
			}
			projection.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, payload.Input, true, toolCallRecoveryDeclared, nil)
		case toolevents.RunningType:
			var payload toolevents.RunningPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.running recovery payload: %w", err)
			}
			projection.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, nil, false, toolCallRecoveryStarted, nil)
		case toolevents.InputResolvedType:
			var payload toolevents.InputResolvedPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.input_resolved recovery payload: %w", err)
			}
			projection.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, payload.Input, true, toolCallRecoveryStarted, nil)
		case toolevents.CompletedType:
			var payload toolevents.CompletedPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.completed recovery payload: %w", err)
			}
			projection.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, nil, false, toolCallRecoveryOutcomeRecorded, payload.Outcome)
		case toolevents.ErroredType:
			var payload toolevents.ErroredPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.errored recovery payload: %w", err)
			}
			projection.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, nil, false, toolCallRecoveryOutcomeRecorded, payload.Outcome)
		}
	}
	return projection, nil
}

func (projection toolCallRecoveryProjection) record(
	turnID string,
	iter, callIndex int,
	messageID, toolUseID, name string,
	input map[string]any,
	replaceInput bool,
	phase toolCallRecoveryPhase,
	outcome *toolevents.RecordedOutcome,
) {
	if messageID == "" || toolUseID == "" {
		return
	}
	key := toolCallRecoveryKey(messageID, toolUseID)
	current := projection[key]
	if current.phase == toolCallRecoveryOutcomeRecorded {
		return
	}
	if !replaceInput {
		input = current.input
	}
	projection[key] = toolCallRecoveryState{
		turnID: turnID, iter: iter, callIndex: callIndex,
		messageID: messageID, toolUseID: toolUseID, name: name, input: input,
		phase: phase, outcome: cloneRecordedOutcome(outcome),
	}
}

func toolCallRecoveryKey(messageID, toolUseID string) string {
	return messageID + "\x00" + toolUseID
}

func cloneRecordedOutcome(outcome *toolevents.RecordedOutcome) *toolevents.RecordedOutcome {
	if outcome == nil {
		return nil
	}
	cloned := *outcome
	return &cloned
}

func decodeRecoveryPayload(payload any, target any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// ProjectTranscriptRepairEvents is the single projection from transcript
// repairs to durable recovery Events. Session loading and Runtime turn startup
// use the same facts while retaining their own Event commit paths.
func ProjectTranscriptRepairEvents(operationTurnID, reason string, repairs []TranscriptRepair) []events.Event {
	if len(repairs) == 0 {
		return nil
	}
	projected := make([]events.Event, 0, len(repairs)+1)
	for _, repair := range repairs {
		if repair.RecoveryCode != "TOOL_OUTCOME_UNKNOWN" || repair.OutcomeUnknownRecorded {
			continue
		}
		call := toolevents.ToolCallPayload{
			Name: repair.ToolName, ToolUseID: repair.ToolUseID,
			Iter: repair.ProviderIteration, CallIndex: repair.CallIndex,
			MessageID: repair.AssistantMessageID, Input: repair.EffectiveInput,
		}
		projected = append(projected, events.Event{
			Type: toolevents.OutcomeUnknownType, TurnID: repair.TurnID,
			Payload: toolevents.OutcomeUnknown(call, toolOutcomeUnknownContent),
		})
	}
	return append(projected, events.Event{
		Type:    "transcript.repaired",
		TurnID:  operationTurnID,
		Payload: TranscriptRepairedPayload{Reason: reason, Repairs: repairs},
	})
}

func (s *Session) appendTranscriptRepairEvents(catalog events.SchemaCatalog, reason string, repairs []TranscriptRepair) error {
	for _, event := range ProjectTranscriptRepairEvents("", reason, repairs) {
		if err := s.appendPreparedRecoveryEvent(catalog, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) appendPreparedRecoveryEvent(catalog events.SchemaCatalog, event events.Event) error {
	prepared, err := catalog.Prepare(events.Normalize(event))
	if err != nil {
		return err
	}
	return s.AppendEvent(prepared)
}
