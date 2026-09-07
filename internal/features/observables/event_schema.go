package observable

import (
	"github.com/juex-ai/juex/internal/foundation/events"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.Ignorable(EventObservableStarted, func() any { return &ObservableEventPayload{} }, true),
		events.Ignorable(EventObservableStopped, func() any { return &ObservableEventPayload{} }, true),
		events.Ignorable(EventObservableExited, func() any { return &ObservableEventPayload{} }, true),
		events.Ignorable(EventObservableErrored, func() any { return &ObservableEventPayload{} }, true),
		events.Ignorable(EventObservationRecorded, func() any { return &ObservationEventPayload{} }, true),
		events.Ignorable(EventObservationQueued, func() any { return &ObservationEventPayload{} }, true),
		events.Ignorable(EventObservationDelivered, func() any { return &ObservationEventPayload{} }, true),
		events.Ignorable(EventObservationDropped, func() any { return &ObservationEventPayload{} }, true),
		events.Ignorable(EventObservationErrored, func() any { return &ObservationEventPayload{} }, true),
	}
}
