package toolevents

import (
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		requiredToolEvent(RequestedType, func() any { return &RequestedPayload{} }, validateRequestedPayload),
		requiredToolEvent(RunningType, func() any { return &RunningPayload{} }, validateRunningPayload),
		events.RequiredValidated(InputResolvedType, 1, func() any { return &InputResolvedPayload{} }, false, validateInputResolvedPayload),
		requiredToolEvent(CompletedType, func() any { return &CompletedPayload{} }, validateCompletedPayload),
		events.TransientVersioned(OutputDeltaType, 2, func() any { return &OutputDeltaPayload{} }),
		requiredToolEvent(ErroredType, func() any { return &ErroredPayload{} }, validateErroredPayload),
		events.RequiredValidated(OutcomeUnknownType, 1, func() any { return &OutcomeUnknownPayload{} }, true, validateOutcomeUnknownPayload),
	}
}

func requiredToolEvent(eventType string, factory func() any, validate func(any) error) events.Definition {
	return events.RequiredValidated(eventType, 2, factory, true, validate)
}

func validateRequestedPayload(payload any) error {
	value, ok := payload.(RequestedPayload)
	if !ok {
		return fmt.Errorf("unexpected requested payload %T", payload)
	}
	return ValidateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex)
}

func validateRunningPayload(payload any) error {
	value, ok := payload.(RunningPayload)
	if !ok {
		return fmt.Errorf("unexpected running payload %T", payload)
	}
	return ValidateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex)
}

func validateInputResolvedPayload(payload any) error {
	value, ok := payload.(InputResolvedPayload)
	if !ok {
		return fmt.Errorf("unexpected input resolved payload %T", payload)
	}
	return ValidateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex)
}

func validateCompletedPayload(payload any) error {
	value, ok := payload.(CompletedPayload)
	if !ok {
		return fmt.Errorf("unexpected completed payload %T", payload)
	}
	if err := ValidateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex); err != nil {
		return err
	}
	return validateRecordedOutcome(value.Name, value.ToolUseID, value.Outcome)
}

func validateErroredPayload(payload any) error {
	value, ok := payload.(ErroredPayload)
	if !ok {
		return fmt.Errorf("unexpected errored payload %T", payload)
	}
	if err := ValidateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex); err != nil {
		return err
	}
	return validateRecordedOutcome(value.Name, value.ToolUseID, value.Outcome)
}

func validateOutcomeUnknownPayload(payload any) error {
	value, ok := payload.(OutcomeUnknownPayload)
	if !ok {
		return fmt.Errorf("unexpected outcome unknown payload %T", payload)
	}
	if err := ValidateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex); err != nil {
		return err
	}
	if value.Error == "" {
		return fmt.Errorf("outcome unknown error is required")
	}
	return nil
}

func ValidateToolIdentity(name, toolUseID, messageID string, iter, callIndex int) error {
	if name == "" || toolUseID == "" || messageID == "" {
		return fmt.Errorf("tool identity requires name, tool_use_id, and message_id")
	}
	if iter < 0 || callIndex < 0 {
		return fmt.Errorf("tool identity iter and call_index must be non-negative")
	}
	return nil
}

func validateRecordedOutcome(name, toolUseID string, outcome *RecordedOutcome) error {
	if outcome == nil || outcome.MessageID == "" {
		return fmt.Errorf("recorded outcome and message_id are required")
	}
	if outcome.Block.Type != llm.BlockToolResult || outcome.Block.ToolUseID != toolUseID || outcome.Block.ToolName != name {
		return fmt.Errorf("recorded outcome block must match tool identity")
	}
	return nil
}
