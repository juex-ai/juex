package thread

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func generationSeedFromState(state ReplayState, providerMessages []llm.Message, contextUsage *llm.ContextUsage) GenerationSeed {
	seed := GenerationSeed{
		Version:          ProjectionVersion,
		ProviderMessages: append([]llm.Message(nil), providerMessages...),
		RecoveryEvents:   generationRecoveryEvents(state.Events),
		CompactionCount:  state.CompactionCount,
		Inputs:           map[string]InputProjection{},
		InputRecords:     map[string]json.RawMessage{},
		ContextUsage:     cloneContextUsage(contextUsage),
	}
	for _, id := range state.InputOrder {
		input := state.Inputs[id]
		if input == nil || input.State.Terminal() {
			continue
		}
		copyInput := *input
		copyInput.Attempts = append([]AttemptProjection(nil), input.Attempts...)
		seed.Inputs[id] = copyInput
		seed.InputOrder = append(seed.InputOrder, id)
		if record := state.InputRecords[id]; len(record) > 0 {
			seed.InputRecords[id] = append(json.RawMessage(nil), record...)
		}
	}
	for id, record := range state.InputRecords {
		if _, exists := seed.InputRecords[id]; exists || !inputRecordReplayable(record) {
			continue
		}
		seed.InputRecords[id] = append(json.RawMessage(nil), record...)
		seed.InputOrder = append(seed.InputOrder, id)
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

func inputRecordReplayable(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var record struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &record) != nil {
		return false
	}
	switch record.State {
	case "accepting", "pending", "admitted":
		return true
	default:
		return false
	}
}
