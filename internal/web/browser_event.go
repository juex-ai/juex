package web

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/toolevents"
)

// BrowserEvent is the stable event DTO sent over the session SSE stream.
// Runtime may persist more event facts than the browser consumes; this DTO is
// the web transport contract for browser-visible read-model updates.
type BrowserEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"ts"`
	TurnID    string          `json:"turn_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type browserPayloadFactory struct {
	typ string
	new func() any
}

var browserPayloadFactories = []browserPayloadFactory{
	{"turn.started", func() any { return &juexruntime.TurnStartedPayload{} }},
	{"turn.completed", func() any { return &juexruntime.TurnCompletedPayload{} }},
	{"turn.errored", func() any { return &juexruntime.TurnErroredPayload{} }},
	{"llm.requested", func() any { return &juexruntime.LLMRequestedPayload{} }},
	{"llm.responded", func() any { return &juexruntime.LLMRespondedPayload{} }},
	{"llm.output_delta", func() any { return &juexruntime.LLMOutputDeltaPayload{} }},
	{"llm.retry", func() any { return &juexruntime.LLMRetryPayload{} }},
	{toolevents.RequestedType, func() any { return &toolevents.RequestedPayload{} }},
	{toolevents.CompletedType, func() any { return &toolevents.CompletedPayload{} }},
	{toolevents.OutputDeltaType, func() any { return &toolevents.OutputDeltaPayload{} }},
	{toolevents.ErroredType, func() any { return &toolevents.ErroredPayload{} }},
	{"hook.started", func() any { return &juexruntime.HookStartedPayload{} }},
	{"hook.completed", func() any { return &juexruntime.HookCompletedPayload{} }},
	{"hook.errored", func() any { return &juexruntime.HookErroredPayload{} }},
	{"hook.trace", func() any { return &juexruntime.HookTracePayload{} }},
	{"pending_input.queued", func() any { return &juexruntime.PendingInputQueuedPayload{} }},
	{"pending_input.drained", func() any { return &juexruntime.PendingInputDrainedPayload{} }},
	{"pending_input.dropped", func() any { return &juexruntime.PendingInputDroppedPayload{} }},
	{"pending_input.rejected", func() any { return &juexruntime.PendingInputRejectedPayload{} }},
	{"goal.updated", func() any { return &juexruntime.GoalUpdatedPayload{} }},
	{observable.EventObservableStarted, func() any { return &observable.ObservableEventPayload{} }},
	{observable.EventObservableStopped, func() any { return &observable.ObservableEventPayload{} }},
	{observable.EventObservableExited, func() any { return &observable.ObservableEventPayload{} }},
	{observable.EventObservableErrored, func() any { return &observable.ObservableEventPayload{} }},
	{observable.EventObservationRecorded, func() any { return &observable.ObservationEventPayload{} }},
	{observable.EventObservationQueued, func() any { return &observable.ObservationEventPayload{} }},
	{observable.EventObservationDelivered, func() any { return &observable.ObservationEventPayload{} }},
	{observable.EventObservationDropped, func() any { return &observable.ObservationEventPayload{} }},
	{observable.EventObservationErrored, func() any { return &observable.ObservationEventPayload{} }},
	{"context.compact.skipped", func() any { return &juexruntime.ContextCompactSkippedPayload{} }},
	{"context.compact.started", func() any { return &juexruntime.ContextCompactStartedPayload{} }},
	{"context.compact.completed", func() any { return &juexruntime.ContextCompactCompletedPayload{} }},
	{"context.compact.errored", func() any { return &juexruntime.ContextCompactErroredPayload{} }},
	{"context.compact.summary_retry", func() any { return &juexruntime.ContextCompactSummaryRetryPayload{} }},
	{"context.compact.summary_model_fallback", func() any { return &juexruntime.ContextCompactSummaryFallbackPayload{} }},
	{"context.projection.applied", func() any { return &BrowserContextProjectionAppliedPayload{} }},
}

var browserPayloadFactoryByType = func() map[string]func() any {
	out := make(map[string]func() any, len(browserPayloadFactories))
	for _, entry := range browserPayloadFactories {
		out[entry.typ] = entry.new
	}
	return out
}()

// BrowserContextProjectionAppliedPayload is the browser-visible shape for the
// runtime context projection event, which is emitted from a small map in the
// runtime to avoid exporting projection internals.
type BrowserContextProjectionAppliedPayload struct {
	UserInputsExternalized        int `json:"user_inputs_externalized"`
	ToolResultsExternalized       int `json:"tool_results_externalized"`
	BytesExternalized             int `json:"bytes_externalized"`
	ReasoningContentsStripped     int `json:"reasoning_contents_stripped,omitempty"`
	ReasoningContentBytesStripped int `json:"reasoning_content_bytes_stripped,omitempty"`
}

func browserEventTypes() []string {
	out := make([]string, 0, len(browserPayloadFactories))
	for _, entry := range browserPayloadFactories {
		out = append(out, entry.typ)
	}
	return out
}

func browserEventFromRuntime(e events.Event) (BrowserEvent, bool, error) {
	factory, ok := browserPayloadFactoryByType[e.Type]
	if !ok {
		return BrowserEvent{}, false, nil
	}
	payload, err := browserPayloadJSON(e.Type, e.Payload, factory)
	if err != nil {
		return BrowserEvent{}, true, err
	}
	return BrowserEvent{
		ID:        e.ID,
		Type:      e.Type,
		Timestamp: e.Timestamp,
		TurnID:    e.TurnID,
		Payload:   payload,
	}, true, nil
}

func browserPayloadJSON(eventType string, payload any, factory func() any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	target := factory()
	targetType := reflect.TypeOf(target)
	payloadType := reflect.TypeOf(payload)
	if browserPayloadTypeMatchesTarget(payloadType, targetType) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal %s browser event payload: %w", eventType, err)
		}
		return raw, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s browser event payload: %w", eventType, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, fmt.Errorf("decode %s browser event payload: %w", eventType, err)
	}
	raw, err = json.Marshal(target)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized %s browser event payload: %w", eventType, err)
	}
	return raw, nil
}

func browserPayloadTypeMatchesTarget(payloadType, targetType reflect.Type) bool {
	if payloadType == targetType {
		return true
	}
	return targetType.Kind() == reflect.Pointer && payloadType == targetType.Elem()
}
