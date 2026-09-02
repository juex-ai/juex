package thread

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

const (
	toolNotStartedContent     = "TOOL_NOT_STARTED: JueX recovered this tool call before execution started. No tool or pre-tool policy was invoked; issue a new tool call if it is still needed."
	toolOutcomeUnknownContent = "TOOL_OUTCOME_UNKNOWN: JueX recorded that this tool call started, but no durable outcome was recorded. It may already have produced external side effects. Do not retry it until the external state has been checked."
)

type ProtocolRepair struct {
	ToolUseID          string         `json:"tool_use_id"`
	ToolName           string         `json:"tool_name,omitempty"`
	RepairMessageID    string         `json:"repair_message_id"`
	Reason             string         `json:"reason,omitempty"`
	TurnID             string         `json:"turn_id,omitempty"`
	ProviderIteration  int            `json:"provider_iteration"`
	CallIndex          int            `json:"call_index"`
	AssistantMessageID string         `json:"assistant_message_id,omitempty"`
	EffectiveInput     map[string]any `json:"effective_input,omitempty"`
	RecoveryCode       string         `json:"recovery_code"`
}

type ProtocolRepairedPayload struct {
	Reason  string           `json:"reason,omitempty"`
	Repairs []ProtocolRepair `json:"repairs"`
}

type recoverableToolCall struct {
	toolevents.ToolCallPayload
	declaredInput map[string]any
	started       bool
	turnID        string
	outcome       *toolevents.RecordedOutcome
}

func (t *Thread) RepairProtocolTail(reason string) ([]ProtocolRepair, error) {
	if t == nil {
		return nil, nil
	}
	t.mu.Lock()
	history := append([]llm.Message(nil), t.History...)
	eventJournal := append([]events.Event(nil), t.state.Events...)
	t.mu.Unlock()

	executions := projectRecoverableToolCalls(eventJournal)
	pending := map[string]recoverableToolCall{}
	var order []string
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolUse && block.ToolUseID != "" {
				call := executions[block.ToolUseID]
				call.ToolUseID = block.ToolUseID
				if call.Name == "" {
					call.Name = block.ToolName
				}
				if call.Input == nil {
					call.Input = block.Input
				}
				if call.declaredInput == nil {
					call.declaredInput = block.Input
				}
				if call.MessageID == "" {
					call.MessageID = message.ID
				}
				if _, exists := pending[block.ToolUseID]; !exists {
					order = append(order, block.ToolUseID)
				}
				pending[block.ToolUseID] = call
			}
			if block.Type == llm.BlockToolResult && block.ToolUseID != "" {
				delete(pending, block.ToolUseID)
			}
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}

	messageID := ""
	for _, id := range order {
		call, exists := pending[id]
		if exists && call.outcome != nil && call.outcome.MessageID != "" {
			messageID = call.outcome.MessageID
			break
		}
	}
	blocks := make([]llm.Block, 0, len(pending))
	repairs := make([]ProtocolRepair, 0, len(pending))
	for _, id := range order {
		call, exists := pending[id]
		if !exists {
			continue
		}
		block, code := recoveredToolResult(call)
		block.ToolUseID = id
		if block.ToolName == "" {
			block.ToolName = call.Name
		}
		blocks = append(blocks, block)
		repairs = append(repairs, ProtocolRepair{
			ToolUseID: id, ToolName: call.Name, Reason: reason,
			TurnID: call.turnID, ProviderIteration: call.Iter, CallIndex: call.CallIndex,
			AssistantMessageID: call.MessageID, EffectiveInput: call.Input, RecoveryCode: code,
		})
	}
	message, err := t.AppendAssigned(llm.Message{ID: messageID, Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: blocks})
	if err != nil {
		return nil, err
	}
	for i := range repairs {
		repairs[i].RepairMessageID = message.ID
	}
	return repairs, nil
}

func recoveredToolResult(call recoverableToolCall) (llm.Block, string) {
	if call.outcome != nil {
		block := call.outcome.Block
		block.Type = llm.BlockToolResult
		return block, "OUTCOME_RECORDED"
	}
	content := toolNotStartedContent
	code := "TOOL_NOT_STARTED"
	if call.started {
		content = toolOutcomeUnknownContent
		if call.Input != nil && !reflect.DeepEqual(call.declaredInput, call.Input) {
			if encoded, err := json.Marshal(call.Input); err == nil {
				content += "\nEffective input at execution: " + string(encoded)
			}
		}
		code = "TOOL_OUTCOME_UNKNOWN"
	}
	return llm.Block{
		Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.Name,
		Content: content, IsError: true,
	}, code
}

func projectRecoverableToolCalls(journal []events.Event) map[string]recoverableToolCall {
	projected := map[string]recoverableToolCall{}
	for _, event := range journal {
		switch event.Type {
		case toolevents.RequestedType:
			var payload toolevents.RequestedPayload
			if decodePayload(event.Payload, &payload) == nil && payload.ToolUseID != "" {
				projected[payload.ToolUseID] = recoverableToolCall{ToolCallPayload: toolevents.ToolCallPayload{
					ToolUseID: payload.ToolUseID, Name: payload.Name, Input: payload.Input,
					TimeoutSeconds: payload.TimeoutSeconds, Iter: payload.Iter,
					CallIndex: payload.CallIndex, MessageID: payload.MessageID,
				}, declaredInput: payload.Input}
			}
		case toolevents.RunningType, toolevents.InputResolvedType:
			var payload struct {
				Name      string         `json:"name"`
				Input     map[string]any `json:"input"`
				ToolUseID string         `json:"tool_use_id"`
				Iter      int            `json:"iter"`
				CallIndex int            `json:"call_index"`
				MessageID string         `json:"message_id"`
			}
			if decodePayload(event.Payload, &payload) == nil && payload.ToolUseID != "" {
				call := projected[payload.ToolUseID]
				call.ToolUseID = payload.ToolUseID
				if payload.Name != "" {
					call.Name = payload.Name
				}
				if payload.Input != nil {
					call.Input = payload.Input
				}
				call.Iter = payload.Iter
				call.CallIndex = payload.CallIndex
				call.MessageID = payload.MessageID
				call.turnID = event.TurnID
				call.started = true
				projected[payload.ToolUseID] = call
			}
		case toolevents.CompletedType:
			var payload toolevents.CompletedPayload
			if decodePayload(event.Payload, &payload) == nil && payload.ToolUseID != "" {
				call := projected[payload.ToolUseID]
				call.ToolUseID = payload.ToolUseID
				call.Name = payload.Name
				call.Iter = payload.Iter
				call.CallIndex = payload.CallIndex
				call.MessageID = payload.MessageID
				call.turnID = event.TurnID
				call.outcome = cloneRecordedOutcome(payload.Outcome)
				projected[payload.ToolUseID] = call
			}
		case toolevents.ErroredType:
			var payload toolevents.ErroredPayload
			if decodePayload(event.Payload, &payload) == nil && payload.ToolUseID != "" {
				call := projected[payload.ToolUseID]
				call.ToolUseID = payload.ToolUseID
				call.Name = payload.Name
				call.Iter = payload.Iter
				call.CallIndex = payload.CallIndex
				call.MessageID = payload.MessageID
				call.turnID = event.TurnID
				call.outcome = cloneRecordedOutcome(payload.Outcome)
				projected[payload.ToolUseID] = call
			}
		case toolevents.OutcomeUnknownType:
			var payload toolevents.OutcomeUnknownPayload
			if decodePayload(event.Payload, &payload) == nil {
				delete(projected, payload.ToolUseID)
			}
		}
	}
	return projected
}

func cloneRecordedOutcome(outcome *toolevents.RecordedOutcome) *toolevents.RecordedOutcome {
	if outcome == nil {
		return nil
	}
	clone := *outcome
	return &clone
}

func ProjectProtocolRepairEvents(operationTurnID, reason string, repairs []ProtocolRepair) []events.Event {
	if len(repairs) == 0 {
		return nil
	}
	projected := make([]events.Event, 0, len(repairs)+1)
	for _, repair := range repairs {
		if repair.RecoveryCode != "TOOL_OUTCOME_UNKNOWN" {
			continue
		}
		projected = append(projected, events.Event{
			Type: toolevents.OutcomeUnknownType, TurnID: repair.TurnID,
			Payload: toolevents.OutcomeUnknown(toolevents.ToolCallPayload{
				ToolUseID: repair.ToolUseID, Name: repair.ToolName, Input: repair.EffectiveInput,
				Iter: repair.ProviderIteration, CallIndex: repair.CallIndex, MessageID: repair.AssistantMessageID,
			}, toolOutcomeUnknownContent),
		})
	}
	return append(projected, events.Event{
		Type: "transcript.repaired", TurnID: operationTurnID,
		Payload: ProtocolRepairedPayload{Reason: reason, Repairs: repairs},
	})
}

func decodePayload(payload, target any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode recovery payload: %w", err)
	}
	return nil
}
