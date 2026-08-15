package session

import (
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/toolevents"
)

type toolExecutionPhase string

const (
	toolExecutionUncheckpointed  toolExecutionPhase = "uncheckpointed"
	toolExecutionDeclared        toolExecutionPhase = "declared"
	toolExecutionStarted         toolExecutionPhase = "started"
	toolExecutionOutcomeRecorded toolExecutionPhase = "outcome-recorded"
)

type toolExecutionRecovery struct {
	turnID    string
	iter      int
	callIndex int
	messageID string
	toolUseID string
	name      string
	phase     toolExecutionPhase
	outcome   *toolevents.RecordedOutcome
}

type toolExecutionRecoveryIndex map[string]toolExecutionRecovery

func (index toolExecutionRecoveryIndex) lookup(messageID, toolUseID string) toolExecutionRecovery {
	if index == nil {
		return toolExecutionRecovery{messageID: messageID, toolUseID: toolUseID, phase: toolExecutionUncheckpointed}
	}
	if state, ok := index[toolExecutionRecoveryKey(messageID, toolUseID)]; ok {
		return state
	}
	return toolExecutionRecovery{messageID: messageID, toolUseID: toolUseID, phase: toolExecutionUncheckpointed}
}

func projectToolExecutionRecovery(journal []events.Event) (toolExecutionRecoveryIndex, error) {
	index := make(toolExecutionRecoveryIndex)
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
				index[toolExecutionRecoveryKey(call.MessageID, call.ToolUseID)] = toolExecutionRecovery{
					turnID: event.TurnID, iter: call.Iter, callIndex: call.CallIndex,
					messageID: call.MessageID, toolUseID: call.ToolUseID, name: call.Name,
					phase: toolExecutionDeclared,
				}
			}
		case toolevents.RequestedType:
			var payload toolevents.RequestedPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.requested recovery payload: %w", err)
			}
			index.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, toolExecutionDeclared, nil)
		case toolevents.RunningType:
			var payload toolevents.RunningPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.running recovery payload: %w", err)
			}
			index.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, toolExecutionStarted, nil)
		case toolevents.CompletedType:
			var payload toolevents.CompletedPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.completed recovery payload: %w", err)
			}
			index.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, toolExecutionOutcomeRecorded, payload.Outcome)
		case toolevents.ErroredType:
			var payload toolevents.ErroredPayload
			if err := decodeRecoveryPayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("session: decode tool.errored recovery payload: %w", err)
			}
			index.record(event.TurnID, payload.Iter, payload.CallIndex, payload.MessageID, payload.ToolUseID, payload.Name, toolExecutionOutcomeRecorded, payload.Outcome)
		}
	}
	return index, nil
}

func (index toolExecutionRecoveryIndex) record(
	turnID string,
	iter, callIndex int,
	messageID, toolUseID, name string,
	phase toolExecutionPhase,
	outcome *toolevents.RecordedOutcome,
) {
	if messageID == "" || toolUseID == "" {
		return
	}
	key := toolExecutionRecoveryKey(messageID, toolUseID)
	current := index[key]
	if current.phase == toolExecutionOutcomeRecorded {
		return
	}
	index[key] = toolExecutionRecovery{
		turnID: turnID, iter: iter, callIndex: callIndex,
		messageID: messageID, toolUseID: toolUseID, name: name,
		phase: phase, outcome: cloneRecordedOutcome(outcome),
	}
}

func toolExecutionRecoveryKey(messageID, toolUseID string) string {
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

func (s *Session) appendTranscriptRepairEvents(catalog events.SchemaCatalog, reason string, repairs []TranscriptRepair) error {
	for _, repair := range repairs {
		if repair.RecoveryCode != "TOOL_OUTCOME_UNKNOWN" {
			continue
		}
		call := toolevents.ToolCallPayload{
			Name: repair.ToolName, ToolUseID: repair.ToolUseID,
			Iter: repair.ProviderIteration, CallIndex: repair.CallIndex,
			MessageID: repair.AssistantMessageID,
		}
		if err := s.appendPreparedRecoveryEvent(catalog, events.Event{
			Type: toolevents.OutcomeUnknownType, TurnID: repair.TurnID,
			Payload: toolevents.OutcomeUnknown(call, toolOutcomeUnknownContent),
		}); err != nil {
			return err
		}
	}
	return s.appendPreparedRecoveryEvent(catalog, events.Event{
		Type:    "transcript.repaired",
		Payload: TranscriptRepairedPayload{Reason: reason, Repairs: repairs},
	})
}

func (s *Session) appendPreparedRecoveryEvent(catalog events.SchemaCatalog, event events.Event) error {
	prepared, err := catalog.Prepare(events.Normalize(event))
	if err != nil {
		return err
	}
	return s.AppendEvent(prepared)
}
