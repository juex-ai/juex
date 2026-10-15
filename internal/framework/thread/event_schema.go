package thread

import (
	"github.com/juex-ai/juex/internal/foundation/events"
)

func EventDefinitions() []events.Definition {
	return []events.Definition{
		events.Ignorable("transcript.repaired", func() any { return &ProtocolRepairedPayload{} }, true),
	}
}
