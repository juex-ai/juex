package notes

import (
	"github.com/juex-ai/juex/internal/foundation/events"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.Ignorable("notes.updated", func() any { return &NotesUpdatedPayload{} }, true),
		events.Ignorable("notes.errored", func() any { return &NotesErroredPayload{} }, true),
	}
}
