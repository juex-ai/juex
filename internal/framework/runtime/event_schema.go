package runtime

import (
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/toolevents"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type ContextProjectionAppliedPayload struct {
	UserInputsExternalized        int `json:"user_inputs_externalized"`
	ToolResultsExternalized       int `json:"tool_results_externalized"`
	BytesExternalized             int `json:"bytes_externalized"`
	ReasoningContentsStripped     int `json:"reasoning_contents_stripped,omitempty"`
	ReasoningContentBytesStripped int `json:"reasoning_content_bytes_stripped,omitempty"`
}

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.Required(TurnAdmittedType, func() any { return &TurnAdmittedPayload{} }, true),
		events.Required("turn.started", func() any { return &TurnStartedPayload{} }, true),
		events.Required(TurnPhaseType, func() any { return &TurnPhasePayload{} }, true),
		events.Required("turn.completed", func() any { return &TurnCompletedPayload{} }, true),
		events.Required("turn.errored", func() any { return &TurnErroredPayload{} }, true),
		events.RequiredValidated("llm.requested", 3, func() any { return &LLMRequestedPayload{} }, true, validateLLMRequestedPayload),
		events.RequiredValidated("llm.responded", 3, func() any { return &LLMRespondedPayload{} }, true, validateLLMRespondedPayload),
		events.RequiredValidated("llm.errored", 2, func() any { return &LLMErroredPayload{} }, true, validateLLMErroredPayload),
		events.Transient("llm.output_delta", func() any { return &LLMOutputDeltaPayload{} }),
		events.RequiredValidated("llm.retry", 3, func() any { return &LLMRetryPayload{} }, true, validateLLMRetryPayload),
		events.Required("llm.fallback", func() any { return &LLMFallbackPayload{} }, true),
		events.IgnorableValidated("policy.requested", func() any { return &PolicyStartedPayload{} }, false, validatePolicyStartedPayload),
		events.IgnorableValidated("policy.started", func() any { return &PolicyStartedPayload{} }, true, validatePolicyStartedPayload),
		events.IgnorableValidated("policy.completed", func() any { return &PolicyCompletedPayload{} }, true, validatePolicyCompletedPayload),
		events.IgnorableValidated("policy.errored", func() any { return &PolicyErroredPayload{} }, true, validatePolicyErroredPayload),
		events.Ignorable("policy.trace", func() any { return &PolicyTracePayload{} }, true),
		events.Required("pending_input.queued", func() any { return &PendingInputQueuedPayload{} }, true),
		events.Required(PendingInputDrainingType, func() any { return &PendingInputDrainingPayload{} }, true),
		events.Required(PendingInputPromotedType, func() any { return &PendingInputPromotedPayload{} }, true),
		events.Required("pending_input.drained", func() any { return &PendingInputDrainedPayload{} }, true),
		events.Required("pending_input.dropped", func() any { return &PendingInputDroppedPayload{} }, true),
		events.Required("pending_input.rejected", func() any { return &PendingInputRejectedPayload{} }, true),
		events.Required("context.compact.skipped", func() any { return &ContextCompactSkippedPayload{} }, true),
		events.Required("context.compact.started", func() any { return &ContextCompactStartedPayload{} }, true),
		events.Required("context.compact.completed", func() any { return &ContextCompactCompletedPayload{} }, true),
		events.Required("context.compact.errored", func() any { return &ContextCompactErroredPayload{} }, true),
		events.IgnorableValidated("context.compact.summary_retry", func() any { return &ContextCompactSummaryRetryPayload{} }, true, validateCompactionSummaryRetryPayload),
		events.IgnorableValidated("context.compact.summary_model_fallback", func() any { return &ContextCompactSummaryFallbackPayload{} }, true, validateCompactionSummaryFallbackPayload),
		events.RequiredValidated("context.compact.summary_responded", 1, func() any { return &ContextCompactSummaryRespondedPayload{} }, false, validateCompactionSummaryRespondedPayload),
		events.RequiredValidated("context.compact.summary_errored", 1, func() any { return &ContextCompactSummaryErroredPayload{} }, false, validateCompactionSummaryErroredPayload),
		events.Ignorable("context.projection.applied", func() any { return &ContextProjectionAppliedPayload{} }, true),
		events.Ignorable("finish.attempted", func() any { return &FinishAttemptedPayload{} }, false),
		events.Ignorable("tool.failure.recorded", func() any { return &ToolFailureRecordedPayload{} }, false),
		events.Ignorable("tool.failure.resolved", func() any { return &ToolFailureResolvedPayload{} }, false),
		events.Ignorable("tool.failure.stale", func() any { return &ToolFailureStalePayload{} }, false),
	}
}

func validateLLMRespondedPayload(payload any) error {
	value, ok := payload.(LLMRespondedPayload)
	if !ok {
		return fmt.Errorf("unexpected llm responded payload %T", payload)
	}
	if value.MessageID == "" || value.Iter < 0 || value.EpochID == "" || value.RequestDigest == "" {
		return fmt.Errorf("llm responded identity requires message_id, epoch_id, request_digest, and a non-negative iter")
	}
	seen := make(map[string]struct{}, len(value.ToolCalls))
	for index, call := range value.ToolCalls {
		if err := toolevents.ValidateToolIdentity(call.Name, call.ToolUseID, call.MessageID, call.Iter, call.CallIndex); err != nil {
			return err
		}
		if call.MessageID != value.MessageID || call.Iter != value.Iter || call.CallIndex != index {
			return fmt.Errorf("llm responded tool identity must match response iteration, message, and order")
		}
		if _, exists := seen[call.ToolUseID]; exists {
			return fmt.Errorf("llm responded tool_use_id %q is duplicated", call.ToolUseID)
		}
		seen[call.ToolUseID] = struct{}{}
	}
	return nil
}

func validateLLMErroredPayload(payload any) error {
	value, ok := payload.(LLMErroredPayload)
	if !ok {
		return fmt.Errorf("unexpected llm errored payload %T", payload)
	}
	if value.Purpose != "turn" {
		return fmt.Errorf("llm errored purpose must be turn")
	}
	if value.Iter < 0 || value.Error == "" || value.EpochID == "" || value.RequestDigest == "" {
		return fmt.Errorf("llm errored identity requires error, epoch_id, request_digest, and a non-negative iter")
	}
	return nil
}

func validateLLMRequestedPayload(payload any) error {
	value, ok := payload.(LLMRequestedPayload)
	if !ok {
		return fmt.Errorf("unexpected llm requested payload %T", payload)
	}
	if value.Purpose != "turn" && value.Purpose != "compaction" {
		return fmt.Errorf("llm requested purpose must be turn or compaction")
	}
	if value.Iter < 0 || value.EpochID == "" || value.RequestDigest == "" {
		return fmt.Errorf("llm requested identity requires purpose, epoch_id, request_digest, and a non-negative iter")
	}
	return nil
}

func validateLLMRetryPayload(payload any) error {
	value, ok := payload.(LLMRetryPayload)
	if !ok {
		return fmt.Errorf("unexpected llm retry payload %T", payload)
	}
	if value.Purpose != "turn" && value.Purpose != "compaction" {
		return fmt.Errorf("llm retry purpose must be turn or compaction")
	}
	if value.EpochID == "" || value.RequestDigest == "" {
		return fmt.Errorf("llm retry requires epoch_id and request_digest")
	}
	if value.Purpose == "turn" && value.Iter == nil {
		return fmt.Errorf("turn llm retry requires iter")
	}
	if value.Purpose == "compaction" && value.Iter != nil {
		return fmt.Errorf("compaction llm retry must not declare a turn iter")
	}
	return nil
}

func validateCompactionSummaryRetryPayload(payload any) error {
	value, ok := payload.(ContextCompactSummaryRetryPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary retry payload %T", payload)
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryFallbackPayload(payload any) error {
	value, ok := payload.(ContextCompactSummaryFallbackPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary fallback payload %T", payload)
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryRespondedPayload(payload any) error {
	value, ok := payload.(ContextCompactSummaryRespondedPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary responded payload %T", payload)
	}
	if value.Attempt <= 0 {
		return fmt.Errorf("compaction summary responded attempt must be positive")
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryErroredPayload(payload any) error {
	value, ok := payload.(ContextCompactSummaryErroredPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary errored payload %T", payload)
	}
	if value.Attempt <= 0 || value.Error == "" {
		return fmt.Errorf("compaction summary errored requires a positive attempt and error")
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryLink(epochID, requestDigest string) error {
	if epochID == "" || requestDigest == "" {
		return fmt.Errorf("compaction summary provenance requires epoch_id and request_digest")
	}
	return nil
}

func validatePolicyStartedPayload(payload any) error {
	value, ok := payload.(PolicyStartedPayload)
	if !ok {
		return fmt.Errorf("unexpected policy started payload %T", payload)
	}
	return validatePolicyIdentity(value.ModuleID, value.PolicyPoint)
}

func validatePolicyCompletedPayload(payload any) error {
	value, ok := payload.(PolicyCompletedPayload)
	if !ok {
		return fmt.Errorf("unexpected policy completed payload %T", payload)
	}
	return validatePolicyIdentity(value.ModuleID, value.PolicyPoint)
}

func validatePolicyErroredPayload(payload any) error {
	value, ok := payload.(PolicyErroredPayload)
	if !ok {
		return fmt.Errorf("unexpected policy errored payload %T", payload)
	}
	if value.Error == "" {
		return fmt.Errorf("policy errored payload requires error")
	}
	return validatePolicyIdentity(value.ModuleID, value.PolicyPoint)
}

func validatePolicyIdentity(moduleID runtimemodule.ID, point runtimemodule.PolicyPoint) error {
	if moduleID == "" {
		return fmt.Errorf("policy lifecycle payload requires module_id and policy_point")
	}
	switch point {
	case runtimemodule.PolicyPointThreadStart,
		runtimemodule.PolicyPointTurnInput,
		runtimemodule.PolicyPointToolBefore,
		runtimemodule.PolicyPointToolAfter,
		runtimemodule.PolicyPointFinish,
		runtimemodule.PolicyPointCompactionBefore,
		runtimemodule.PolicyPointCompactionAfter:
		return nil
	default:
		return fmt.Errorf("policy lifecycle payload has invalid policy_point %q", point)
	}
}
