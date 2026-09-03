package thread

import (
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func generationSeedFromState(state ReplayState, providerMessages []llm.Message, contextUsage *llm.ContextUsage) GenerationSeed {
	seed := GenerationSeed{
		Version:          ProjectionVersion,
		ProviderMessages: append([]llm.Message(nil), providerMessages...),
		RecoveryEvents:   generationRecoveryEvents(state.Events),
		CompactionCount:  state.CompactionCount,
		ContextUsage:     cloneContextUsage(contextUsage),
	}
	return seed
}

// generationRecoveryEvents retains the latest settled status plus everything
// after it. During a working Turn this includes the tool request/running/
// outcome provenance required for conservative protocol repair after restart.
func generationRecoveryEvents(recorded []events.Event) []events.Event {
	start := 0
	for index := len(recorded) - 1; index >= 0; index-- {
		switch recorded[index].Type {
		case "turn.completed", "turn.errored", "turn.cancelled":
			start = index
			return append([]events.Event(nil), recorded[start:]...)
		}
	}
	return append([]events.Event(nil), recorded...)
}
