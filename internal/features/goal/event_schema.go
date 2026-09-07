package goal

import (
	"github.com/juex-ai/juex/internal/foundation/events"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.Ignorable("goal.updated", func() any { return &GoalUpdatedPayload{} }, true),
		events.Ignorable("goal.continued", func() any { return &GoalContinuedPayload{} }, false),
	}
}
