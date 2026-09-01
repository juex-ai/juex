package thread

import (
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const checkpointInterval = uint64(256)

func checkpointFromState(state ReplayState) ReplayCheckpoint {
	checkpoint := ReplayCheckpoint{
		Version:          ProjectionVersion,
		Projection:       cloneProjection(state.Projection),
		ProviderMessages: append([]llm.Message(nil), state.ProviderMessages...),
		Inputs:           map[string]InputProjection{},
		InputRecords:     map[string]json.RawMessage{},
		ContextUsage:     cloneContextUsage(state.ContextUsage),
	}
	if terminal := latestTerminalStatusEvent(state.Events); terminal != nil {
		checkpoint.StatusEvents = []events.Event{*terminal}
	}
	if len(state.Activities) > 0 {
		activity := state.Activities[len(state.Activities)-1]
		checkpoint.LatestActivity = &activity
	}
	for _, id := range state.InputOrder {
		input := state.Inputs[id]
		if input == nil || input.State.Terminal() {
			continue
		}
		copyInput := *input
		copyInput.Attempts = append([]AttemptProjection(nil), input.Attempts...)
		checkpoint.Inputs[id] = copyInput
		checkpoint.InputOrder = append(checkpoint.InputOrder, id)
		if record := state.InputRecords[id]; len(record) > 0 {
			checkpoint.InputRecords[id] = append(json.RawMessage(nil), record...)
		}
	}
	for id, record := range state.InputRecords {
		if _, exists := checkpoint.InputRecords[id]; exists || !inputRecordReplayable(record) {
			continue
		}
		checkpoint.InputRecords[id] = append(json.RawMessage(nil), record...)
		checkpoint.InputOrder = append(checkpoint.InputOrder, id)
	}
	return checkpoint
}

func replayStateFromCheckpoint(threadID string, scanned scannedCommit, checkpoint ReplayCheckpoint) (ReplayState, error) {
	if checkpoint.Version != ProjectionVersion || checkpoint.Projection.Version != ProjectionVersion {
		return ReplayState{}, fmt.Errorf("%w: unsupported checkpoint version", ErrCorruptJournal)
	}
	projection := checkpoint.Projection
	if projection.ThreadID != threadID || projection.Revision+1 != scanned.Seq ||
		projection.Journal.ProjectedSeq != projection.Revision ||
		projection.Journal.ProjectedOffset != scanned.StartOffset {
		return ReplayState{}, fmt.Errorf("%w: checkpoint sequence or Thread identity mismatch", ErrCorruptJournal)
	}
	state := ReplayState{
		Projection:       cloneProjection(projection),
		Messages:         append([]llm.Message(nil), checkpoint.ProviderMessages...),
		ProviderMessages: append([]llm.Message(nil), checkpoint.ProviderMessages...),
		Inputs:           make(map[string]*InputProjection, len(checkpoint.Inputs)),
		InputOrder:       append([]string(nil), checkpoint.InputOrder...),
		InputRecords:     make(map[string]json.RawMessage, len(checkpoint.InputRecords)),
		ContextUsage:     cloneContextUsage(checkpoint.ContextUsage),
		Events:           append([]events.Event(nil), checkpoint.StatusEvents...),
	}
	if checkpoint.LatestActivity != nil {
		state.Activities = []Activity{*checkpoint.LatestActivity}
	}
	for id, input := range checkpoint.Inputs {
		copyInput := input
		copyInput.Attempts = append([]AttemptProjection(nil), input.Attempts...)
		state.Inputs[id] = &copyInput
	}
	for id, record := range checkpoint.InputRecords {
		state.InputRecords[id] = append(json.RawMessage(nil), record...)
	}
	if err := applyCommit(threadID, &state, scanned); err != nil {
		return ReplayState{}, fmt.Errorf("%w at checkpoint sequence %d: %v", ErrCorruptJournal, scanned.Seq, err)
	}
	return state, nil
}

func latestTerminalStatusEvent(recorded []events.Event) *events.Event {
	for index := len(recorded) - 1; index >= 0; index-- {
		event := recorded[index]
		switch event.Type {
		case "turn.completed", "turn.errored", "turn.cancelled":
			return &event
		}
	}
	return nil
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

func factsRequireCheckpoint(facts []Fact) bool {
	for _, fact := range facts {
		switch fact.Type {
		case FactTurnCompleted, FactTurnFailed, FactTurnCancelled,
			FactContextRenewed, FactContextCompacted,
			FactThreadArchived, FactThreadUnarchived:
			return true
		}
	}
	return false
}

func shouldAppendCheckpoint(state ReplayState, commit Commit) bool {
	if len(commit.Facts) == 1 && commit.Facts[0].Type == FactProjectionCheck {
		return false
	}
	if state.Projection.State == StateWorking {
		return false
	}
	last := state.Projection.Journal.LastCheckpointSeq
	return factsRequireCheckpoint(commit.Facts) || commit.Seq-last >= checkpointInterval
}
