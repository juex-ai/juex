package tasks

import (
	"github.com/juex-ai/juex/internal/foundation/events"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.Ignorable("tasks.updated", func() any { return &TasksUpdatedPayload{} }, true),
		events.Ignorable("tasks.continued", func() any { return &TaskContinuedPayload{} }, false),
	}
}
