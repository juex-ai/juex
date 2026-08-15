package eventcatalog

import (
	"sync"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/observable"
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
		required("llm.requested", func() any { return &juexruntime.LLMRequestedPayload{} }, true),
		required("llm.responded", func() any { return &juexruntime.LLMRespondedPayload{} }, true),
		transient("llm.output_delta", func() any { return &juexruntime.LLMOutputDeltaPayload{} }),
		required("llm.retry", func() any { return &juexruntime.LLMRetryPayload{} }, true),
		required("llm.fallback", func() any { return &juexruntime.LLMFallbackPayload{} }, true),
		required(toolevents.RequestedType, func() any { return &toolevents.RequestedPayload{} }, true),
		required(toolevents.RunningType, func() any { return &toolevents.RunningPayload{} }, true),
		required(toolevents.CompletedType, func() any { return &toolevents.CompletedPayload{} }, true),
		transient(toolevents.OutputDeltaType, func() any { return &toolevents.OutputDeltaPayload{} }),
		required(toolevents.ErroredType, func() any { return &toolevents.ErroredPayload{} }, true),
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
		ignorable("context.compact.summary_retry", func() any { return &juexruntime.ContextCompactSummaryRetryPayload{} }, true),
		ignorable("context.compact.summary_model_fallback", func() any { return &juexruntime.ContextCompactSummaryFallbackPayload{} }, true),
		ignorable("context.projection.applied", func() any { return &ContextProjectionAppliedPayload{} }, true),
		ignorable("finish.attempted", func() any { return &juexruntime.FinishAttemptedPayload{} }, false),
		ignorable("tool.failure.recorded", func() any { return &juexruntime.ToolFailureRecordedPayload{} }, false),
		ignorable("tool.failure.resolved", func() any { return &juexruntime.ToolFailureResolvedPayload{} }, false),
		ignorable("tool.failure.stale", func() any { return &juexruntime.ToolFailureStalePayload{} }, false),
		ignorable("transcript.repaired", func() any { return &session.TranscriptRepairedPayload{} }, false),
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

func transient(eventType string, factory func() any) Definition {
	return Definition{
		Type: eventType, Version: 1,
		Transient: true, BrowserVisible: true, NewPayload: factory,
	}
}
