package provenance

import (
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/events"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.RequiredValidated(RequestEpochType, 1, func() any { return &RequestEpochPayload{} }, true, validateRequestEpochPayload),
		events.RequiredValidated(PolicyContextQueuedType, 1, func() any { return &PolicyContextQueuedPayload{} }, false, validatePolicyContextQueuedPayload),
	}
}

func validateRequestEpochPayload(payload any) error {
	value, ok := payload.(RequestEpochPayload)
	if !ok {
		return fmt.Errorf("unexpected request epoch payload %T", payload)
	}
	return ValidateRequestEpoch(value)
}

func validatePolicyContextQueuedPayload(payload any) error {
	value, ok := payload.(PolicyContextQueuedPayload)
	if !ok {
		return fmt.Errorf("unexpected policy context queued payload %T", payload)
	}
	return ValidatePolicyContextQueued(value)
}
