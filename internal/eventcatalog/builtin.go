package eventcatalog

import (
	"fmt"
	"sync"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/provenance"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
)

type ContextProjectionAppliedPayload struct {
	UserInputsExternalized        int `json:"user_inputs_externalized"`
	ToolResultsExternalized       int `json:"tool_results_externalized"`
	BytesExternalized             int `json:"bytes_externalized"`
	ReasoningContentsStripped     int `json:"reasoning_contents_stripped,omitempty"`
	ReasoningContentBytesStripped int `json:"reasoning_content_bytes_stripped,omitempty"`
}

var (
	defaultOnce    sync.Once
	defaultCatalog *Catalog
)

func Default() *Catalog {
	defaultOnce.Do(func() {
		var err error
		defaultCatalog, err = New(builtinDefinitions()...)
		if err != nil {
			panic(err)
		}
	})
	return defaultCatalog
}

func builtinDefinitions() []Definition {
	return []Definition{
		required(juexruntime.TurnAdmittedType, func() any { return &juexruntime.TurnAdmittedPayload{} }, true),
		required("turn.started", func() any { return &juexruntime.TurnStartedPayload{} }, true),
		required(juexruntime.TurnPhaseType, func() any { return &juexruntime.TurnPhasePayload{} }, true),
		required("turn.completed", func() any { return &juexruntime.TurnCompletedPayload{} }, true),
		required("turn.errored", func() any { return &juexruntime.TurnErroredPayload{} }, true),
		requiredValidated("llm.requested", 3, func() any { return &juexruntime.LLMRequestedPayload{} }, true, validateLLMRequestedPayload),
		requiredValidated("llm.responded", 3, func() any { return &juexruntime.LLMRespondedPayload{} }, true, validateLLMRespondedPayload),
		requiredValidated("llm.errored", 1, func() any { return &juexruntime.LLMErroredPayload{} }, true, validateLLMErroredPayload),
		requiredValidated(provenance.RequestEpochType, 1, func() any { return &provenance.RequestEpochPayload{} }, true, validateRequestEpochPayload),
		requiredValidated(provenance.HookContextQueuedType, 1, func() any { return &provenance.HookContextQueuedPayload{} }, false, validateHookContextQueuedPayload),
		transient("llm.output_delta", func() any { return &juexruntime.LLMOutputDeltaPayload{} }),
		requiredValidated("llm.retry", 3, func() any { return &juexruntime.LLMRetryPayload{} }, true, validateLLMRetryPayload),
		required("llm.fallback", func() any { return &juexruntime.LLMFallbackPayload{} }, true),
		requiredToolEvent(toolevents.RequestedType, func() any { return &toolevents.RequestedPayload{} }, validateRequestedPayload),
		requiredToolEvent(toolevents.RunningType, func() any { return &toolevents.RunningPayload{} }, validateRunningPayload),
		requiredValidated(toolevents.InputResolvedType, 1, func() any { return &toolevents.InputResolvedPayload{} }, false, validateInputResolvedPayload),
		requiredToolEvent(toolevents.CompletedType, func() any { return &toolevents.CompletedPayload{} }, validateCompletedPayload),
		transientVersioned(toolevents.OutputDeltaType, 2, func() any { return &toolevents.OutputDeltaPayload{} }),
		requiredToolEvent(toolevents.ErroredType, func() any { return &toolevents.ErroredPayload{} }, validateErroredPayload),
		requiredValidated(toolevents.OutcomeUnknownType, 1, func() any { return &toolevents.OutcomeUnknownPayload{} }, true, validateOutcomeUnknownPayload),
		ignorable("hook.requested", func() any { return &juexruntime.HookStartedPayload{} }, false),
		ignorable("hook.started", func() any { return &juexruntime.HookStartedPayload{} }, true),
		ignorable("hook.completed", func() any { return &juexruntime.HookCompletedPayload{} }, true),
		ignorable("hook.errored", func() any { return &juexruntime.HookErroredPayload{} }, true),
		ignorable("hook.trace", func() any { return &juexruntime.HookTracePayload{} }, true),
		required("pending_input.queued", func() any { return &juexruntime.PendingInputQueuedPayload{} }, true),
		required(juexruntime.PendingInputDrainingType, func() any { return &juexruntime.PendingInputDrainingPayload{} }, true),
		required(juexruntime.PendingInputPromotedType, func() any { return &juexruntime.PendingInputPromotedPayload{} }, true),
		required("pending_input.drained", func() any { return &juexruntime.PendingInputDrainedPayload{} }, true),
		required("pending_input.dropped", func() any { return &juexruntime.PendingInputDroppedPayload{} }, true),
		required("pending_input.rejected", func() any { return &juexruntime.PendingInputRejectedPayload{} }, true),
		ignorable("goal.updated", func() any { return &juexruntime.GoalUpdatedPayload{} }, true),
		ignorable("goal.continued", func() any { return &juexruntime.GoalContinuedPayload{} }, false),
		ignorable("notes.updated", func() any { return &juexruntime.NotesUpdatedPayload{} }, true),
		ignorable("notes.errored", func() any { return &juexruntime.NotesErroredPayload{} }, true),
		ignorable(observable.EventObservableStarted, func() any { return &observable.ObservableEventPayload{} }, true),
		ignorable(observable.EventObservableStopped, func() any { return &observable.ObservableEventPayload{} }, true),
		ignorable(observable.EventObservableExited, func() any { return &observable.ObservableEventPayload{} }, true),
		ignorable(observable.EventObservableErrored, func() any { return &observable.ObservableEventPayload{} }, true),
		ignorable(observable.EventObservationRecorded, func() any { return &observable.ObservationEventPayload{} }, true),
		ignorable(observable.EventObservationQueued, func() any { return &observable.ObservationEventPayload{} }, true),
		ignorable(observable.EventObservationDelivered, func() any { return &observable.ObservationEventPayload{} }, true),
		ignorable(observable.EventObservationDropped, func() any { return &observable.ObservationEventPayload{} }, true),
		ignorable(observable.EventObservationErrored, func() any { return &observable.ObservationEventPayload{} }, true),
		required("context.compact.skipped", func() any { return &juexruntime.ContextCompactSkippedPayload{} }, true),
		required("context.compact.started", func() any { return &juexruntime.ContextCompactStartedPayload{} }, true),
		required("context.compact.completed", func() any { return &juexruntime.ContextCompactCompletedPayload{} }, true),
		required("context.compact.errored", func() any { return &juexruntime.ContextCompactErroredPayload{} }, true),
		ignorableValidated("context.compact.summary_retry", func() any { return &juexruntime.ContextCompactSummaryRetryPayload{} }, true, validateCompactionSummaryRetryPayload),
		ignorableValidated("context.compact.summary_model_fallback", func() any { return &juexruntime.ContextCompactSummaryFallbackPayload{} }, true, validateCompactionSummaryFallbackPayload),
		requiredValidated("context.compact.summary_responded", 1, func() any { return &juexruntime.ContextCompactSummaryRespondedPayload{} }, false, validateCompactionSummaryRespondedPayload),
		requiredValidated("context.compact.summary_errored", 1, func() any { return &juexruntime.ContextCompactSummaryErroredPayload{} }, false, validateCompactionSummaryErroredPayload),
		ignorable("context.projection.applied", func() any { return &ContextProjectionAppliedPayload{} }, true),
		ignorable("finish.attempted", func() any { return &juexruntime.FinishAttemptedPayload{} }, false),
		ignorable("tool.failure.recorded", func() any { return &juexruntime.ToolFailureRecordedPayload{} }, false),
		ignorable("tool.failure.resolved", func() any { return &juexruntime.ToolFailureResolvedPayload{} }, false),
		ignorable("tool.failure.stale", func() any { return &juexruntime.ToolFailureStalePayload{} }, false),
		ignorable("transcript.repaired", func() any { return &session.TranscriptRepairedPayload{} }, true),
	}
}

func required(eventType string, factory func() any, browserVisible bool) Definition {
	return Definition{
		Type: eventType, Version: 1, ReplayPolicy: events.ReplayRequired,
		BrowserVisible: browserVisible, NewPayload: factory,
	}
}

func ignorable(eventType string, factory func() any, browserVisible bool) Definition {
	return Definition{
		Type: eventType, Version: 1, ReplayPolicy: events.ReplayIgnorable,
		BrowserVisible: browserVisible, NewPayload: factory,
	}
}

func ignorableValidated(eventType string, factory func() any, browserVisible bool, validate func(any) error) Definition {
	return Definition{
		Type: eventType, Version: 1, ReplayPolicy: events.ReplayIgnorable,
		BrowserVisible: browserVisible, NewPayload: factory, Validate: validate,
	}
}

func transient(eventType string, factory func() any) Definition {
	return transientVersioned(eventType, 1, factory)
}

func transientVersioned(eventType string, version int, factory func() any) Definition {
	return Definition{
		Type: eventType, Version: version,
		Transient: true, BrowserVisible: true, NewPayload: factory,
	}
}

func requiredToolEvent(eventType string, factory func() any, validate func(any) error) Definition {
	return requiredValidated(eventType, 2, factory, true, validate)
}

func requiredValidated(eventType string, version int, factory func() any, browserVisible bool, validate func(any) error) Definition {
	return Definition{
		Type: eventType, Version: version, ReplayPolicy: events.ReplayRequired,
		BrowserVisible: browserVisible, NewPayload: factory, Validate: validate,
	}
}

func validateRequestedPayload(payload any) error {
	value, ok := payload.(toolevents.RequestedPayload)
	if !ok {
		return fmt.Errorf("unexpected requested payload %T", payload)
	}
	return validateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex)
}

func validateLLMRespondedPayload(payload any) error {
	value, ok := payload.(juexruntime.LLMRespondedPayload)
	if !ok {
		return fmt.Errorf("unexpected llm responded payload %T", payload)
	}
	if value.MessageID == "" || value.Iter < 0 || value.EpochID == "" || value.RequestDigest == "" {
		return fmt.Errorf("llm responded identity requires message_id, epoch_id, request_digest, and a non-negative iter")
	}
	seen := make(map[string]struct{}, len(value.ToolCalls))
	for index, call := range value.ToolCalls {
		if err := validateToolIdentity(call.Name, call.ToolUseID, call.MessageID, call.Iter, call.CallIndex); err != nil {
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
	value, ok := payload.(juexruntime.LLMErroredPayload)
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
	value, ok := payload.(juexruntime.LLMRequestedPayload)
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
	value, ok := payload.(juexruntime.LLMRetryPayload)
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
	value, ok := payload.(juexruntime.ContextCompactSummaryRetryPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary retry payload %T", payload)
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryFallbackPayload(payload any) error {
	value, ok := payload.(juexruntime.ContextCompactSummaryFallbackPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary fallback payload %T", payload)
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryRespondedPayload(payload any) error {
	value, ok := payload.(juexruntime.ContextCompactSummaryRespondedPayload)
	if !ok {
		return fmt.Errorf("unexpected compaction summary responded payload %T", payload)
	}
	if value.Attempt <= 0 {
		return fmt.Errorf("compaction summary responded attempt must be positive")
	}
	return validateCompactionSummaryLink(value.EpochID, value.RequestDigest)
}

func validateCompactionSummaryErroredPayload(payload any) error {
	value, ok := payload.(juexruntime.ContextCompactSummaryErroredPayload)
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

func validateRequestEpochPayload(payload any) error {
	value, ok := payload.(provenance.RequestEpochPayload)
	if !ok {
		return fmt.Errorf("unexpected request epoch payload %T", payload)
	}
	return provenance.ValidateRequestEpoch(value)
}

func validateHookContextQueuedPayload(payload any) error {
	value, ok := payload.(provenance.HookContextQueuedPayload)
	if !ok {
		return fmt.Errorf("unexpected hook context queued payload %T", payload)
	}
	return provenance.ValidateHookContextQueued(value)
}

func validateRunningPayload(payload any) error {
	value, ok := payload.(toolevents.RunningPayload)
	if !ok {
		return fmt.Errorf("unexpected running payload %T", payload)
	}
	return validateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex)
}

func validateInputResolvedPayload(payload any) error {
	value, ok := payload.(toolevents.InputResolvedPayload)
	if !ok {
		return fmt.Errorf("unexpected input resolved payload %T", payload)
	}
	return validateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex)
}

func validateCompletedPayload(payload any) error {
	value, ok := payload.(toolevents.CompletedPayload)
	if !ok {
		return fmt.Errorf("unexpected completed payload %T", payload)
	}
	if err := validateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex); err != nil {
		return err
	}
	return validateRecordedOutcome(value.Name, value.ToolUseID, value.Outcome)
}

func validateErroredPayload(payload any) error {
	value, ok := payload.(toolevents.ErroredPayload)
	if !ok {
		return fmt.Errorf("unexpected errored payload %T", payload)
	}
	if err := validateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex); err != nil {
		return err
	}
	return validateRecordedOutcome(value.Name, value.ToolUseID, value.Outcome)
}

func validateOutcomeUnknownPayload(payload any) error {
	value, ok := payload.(toolevents.OutcomeUnknownPayload)
	if !ok {
		return fmt.Errorf("unexpected outcome unknown payload %T", payload)
	}
	if err := validateToolIdentity(value.Name, value.ToolUseID, value.MessageID, value.Iter, value.CallIndex); err != nil {
		return err
	}
	if value.Error == "" {
		return fmt.Errorf("outcome unknown error is required")
	}
	return nil
}

func validateToolIdentity(name, toolUseID, messageID string, iter, callIndex int) error {
	if name == "" || toolUseID == "" || messageID == "" {
		return fmt.Errorf("tool identity requires name, tool_use_id, and message_id")
	}
	if iter < 0 || callIndex < 0 {
		return fmt.Errorf("tool identity iter and call_index must be non-negative")
	}
	return nil
}

func validateRecordedOutcome(name, toolUseID string, outcome *toolevents.RecordedOutcome) error {
	if outcome == nil || outcome.MessageID == "" {
		return fmt.Errorf("recorded outcome and message_id are required")
	}
	if outcome.Block.Type != llm.BlockToolResult || outcome.Block.ToolUseID != toolUseID || outcome.Block.ToolName != name {
		return fmt.Errorf("recorded outcome block must match tool identity")
	}
	return nil
}
