package thread

import (
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
)

func replay(threadID string, commits []scannedCommit) (ReplayState, error) {
	state := ReplayState{Inputs: map[string]*InputProjection{}, InputRecords: map[string]json.RawMessage{}}
	for _, commit := range commits {
		if err := applyCommit(threadID, &state, commit); err != nil {
			return ReplayState{}, fmt.Errorf("%w at sequence %d: %v", ErrCorruptJournal, commit.Seq, err)
		}
	}
	if len(commits) == 0 {
		return ReplayState{}, fmt.Errorf("%w: missing thread.created", ErrCorruptJournal)
	}
	return state, nil
}

func applyCommit(threadID string, state *ReplayState, commit scannedCommit) error {
	if state == nil {
		return fmt.Errorf("%w: nil replay state", ErrInvalidTransition)
	}
	for _, fact := range commit.Facts {
		if err := applyFact(threadID, state, commit, fact); err != nil {
			return err
		}
	}
	if err := validateProjectionLifecycle(state.Projection); err != nil {
		return err
	}
	if len(commit.Facts) != 1 || commit.Facts[0].Type != FactProjectionCheck {
		state.Projection.LastActivityAt = commit.At
	}
	state.Projection.UpdatedAt = commit.At
	state.Projection.Journal.ProjectedSeq = commit.Seq
	state.Projection.Journal.ProjectedOffset = commit.EndOffset
	return nil
}

func validateProjectionLifecycle(projection Projection) error {
	switch projection.RetentionState {
	case RetentionActive:
		if projection.ArchivedAt != nil {
			return fmt.Errorf("%w: active Thread has archived_at", ErrInvalidTransition)
		}
		switch projection.ExecutionState {
		case ExecutionIdle, ExecutionWorking, ExecutionFailed:
			return nil
		default:
			return fmt.Errorf("%w: active Thread has execution state %q", ErrInvalidTransition, projection.ExecutionState)
		}
	case RetentionArchived:
		if projection.ArchivedAt == nil {
			return fmt.Errorf("%w: archived Thread lacks archived_at", ErrInvalidTransition)
		}
		if projection.ExecutionState != "" {
			return fmt.Errorf("%w: archived Thread has execution state %q", ErrInvalidTransition, projection.ExecutionState)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown retention state %q", ErrInvalidTransition, projection.RetentionState)
	}
}

func applyFact(threadID string, state *ReplayState, commit scannedCommit, fact Fact) error {
	p := &state.Projection
	switch fact.Type {
	case FactThreadCreated:
		if p.ThreadID != "" || commit.Seq != 1 {
			return fmt.Errorf("%w: duplicate or late thread.created", ErrInvalidTransition)
		}
		p.Version = ProjectionVersion
		p.ThreadID = threadID
		p.Alias = fact.Alias
		p.ParentThreadID = fact.ParentThreadID
		p.CreatedAt = commit.At
		p.UpdatedAt = commit.At
		p.LastActivityAt = commit.At
		p.RetentionState = RetentionActive
		p.ExecutionState = ExecutionIdle
		p.CurrentGeneration = GenerationProjection{
			ID:          InitialGeneration,
			Ordinal:     1,
			StartSeq:    commit.Seq,
			StartOffset: commit.StartOffset,
		}
		p.Generations = []GenerationProjection{p.CurrentGeneration}
		p.Counts.GenerationCount = 1
	case FactMessageAppended:
		if fact.GenerationID != p.CurrentGeneration.ID {
			return fmt.Errorf("%w: message generation %q is not current %q", ErrInvalidTransition, fact.GenerationID, p.CurrentGeneration.ID)
		}
		message := *fact.Message
		state.Messages = append(state.Messages, message)
		state.ProviderMessages = append(state.ProviderMessages, message)
	case FactEventRecorded:
		event := *fact.Event
		state.Events = append(state.Events, event)
	case FactInputAccepted:
		if _, exists := state.Inputs[fact.InputID]; exists {
			return fmt.Errorf("%w: duplicate accepted input %q", ErrInvalidTransition, fact.InputID)
		}
		state.Inputs[fact.InputID] = &InputProjection{ID: fact.InputID, State: InputAccepted}
		p.Counts.PendingInputCount++
	case FactInputRecorded:
		var recorded struct {
			ID    string `json:"id"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(fact.InputRecord, &recorded); err != nil || recorded.ID != fact.InputID {
			return fmt.Errorf("%w: invalid input record %q", ErrInvalidTransition, fact.InputID)
		}
		if _, recordedBefore := state.InputRecords[fact.InputID]; !recordedBefore {
			state.InputOrder = append(state.InputOrder, fact.InputID)
		}
		if state.Inputs[fact.InputID] == nil {
			previousPending := inputRecordPending(state.InputRecords[fact.InputID])
			currentPending := recorded.State == "pending"
			if !previousPending && currentPending {
				p.Counts.PendingInputCount++
			} else if previousPending && !currentPending {
				p.Counts.PendingInputCount--
			}
		}
		state.InputRecords[fact.InputID] = append(json.RawMessage(nil), fact.InputRecord...)
	case FactInputAttemptStart:
		input, err := requireOpenInput(state, fact.InputID)
		if err != nil {
			return err
		}
		if fact.GenerationID != p.CurrentGeneration.ID {
			return fmt.Errorf("%w: attempt generation is not current", ErrInvalidTransition)
		}
		for _, attempt := range input.Attempts {
			if attempt.ID == fact.AttemptID {
				return fmt.Errorf("%w: duplicate attempt %q", ErrInvalidTransition, fact.AttemptID)
			}
		}
		input.Attempts = append(input.Attempts, AttemptProjection{
			ID:           fact.AttemptID,
			GenerationID: fact.GenerationID,
			TurnID:       fact.TurnID,
			State:        "running",
		})
		input.State = InputRunning
		if p.Counts.PendingInputCount > 0 {
			p.Counts.PendingInputCount--
		}
		p.ExecutionState = ExecutionWorking
	case FactInputAttemptDone, FactInputAttemptFailed, FactInputAttemptCancel, FactInputAttemptStop:
		input, attempt, err := requireRunningAttempt(state, fact.InputID, fact.AttemptID)
		if err != nil {
			return err
		}
		switch fact.Type {
		case FactInputAttemptDone:
			attempt.State = "succeeded"
		case FactInputAttemptFailed:
			attempt.State = "failed"
			input.State = InputRetryable
			p.Counts.PendingInputCount++
		case FactInputAttemptCancel:
			attempt.State = "cancelled"
			input.State = InputRetryable
			p.Counts.PendingInputCount++
		case FactInputAttemptStop:
			attempt.State = "interrupted"
			input.State = InputRetryable
			p.Counts.PendingInputCount++
		}
		attempt.Error = fact.Error
	case FactInputRequeued:
		input, err := requireOpenInput(state, fact.InputID)
		if err != nil {
			return err
		}
		if input.State != InputRetryable {
			return fmt.Errorf("%w: input %q is not retryable", ErrInvalidTransition, fact.InputID)
		}
		input.State = InputAccepted
	case FactInputCompleted, FactInputDeadLettered, FactInputCancelled, FactInputExpired:
		input, err := requireOpenInput(state, fact.InputID)
		if err != nil {
			return err
		}
		wasPending := input.State == InputAccepted || input.State == InputRetryable
		switch fact.Type {
		case FactInputCompleted:
			if input.State == InputRunning {
				last := &input.Attempts[len(input.Attempts)-1]
				if last.State != "succeeded" {
					return fmt.Errorf("%w: input completion requires succeeded attempt", ErrInvalidTransition)
				}
			}
			input.State = InputCompleted
		case FactInputDeadLettered:
			input.State = InputDeadLettered
		case FactInputCancelled:
			input.State = InputCancelled
		case FactInputExpired:
			input.State = InputExpired
		}
		if wasPending {
			p.Counts.PendingInputCount--
		}
	case FactTurnStarted:
		p.ExecutionState = ExecutionWorking
	case FactTurnCompleted:
		p.Counts.TurnCount++
	case FactTurnFailed:
		p.Counts.TurnCount++
		p.ExecutionState = ExecutionFailed
	case FactTurnCancelled:
		p.Counts.TurnCount++
	case FactThreadSettled:
		if p.RetentionState == RetentionActive {
			p.ExecutionState = ExecutionIdle
		}
	case FactContextRenewed, FactContextCompacted:
		if fact.FromGenerationID != p.CurrentGeneration.ID {
			return fmt.Errorf("%w: generation source %q is not current %q", ErrInvalidTransition, fact.FromGenerationID, p.CurrentGeneration.ID)
		}
		nextOrdinal, err := parseGenerationID(fact.ToGenerationID)
		if err != nil || nextOrdinal != p.CurrentGeneration.Ordinal+1 {
			return fmt.Errorf("%w: generation target %q is not next", ErrInvalidTransition, fact.ToGenerationID)
		}
		activity := Activity{
			Type:             fact.Type,
			At:               commit.At,
			FromGenerationID: fact.FromGenerationID,
			ToGenerationID:   fact.ToGenerationID,
			Summary:          fact.Summary,
			Automatic:        fact.Automatic,
		}
		state.Activities = append(state.Activities, activity)
		if fact.Type == FactContextCompacted {
			state.CompactionCount++
			state.ProviderMessages = compactProviderMessages(*fact.Summary, state.Messages)
		} else {
			state.ProviderMessages = nil
		}
		p.CurrentGeneration = GenerationProjection{
			ID:          fact.ToGenerationID,
			Ordinal:     nextOrdinal,
			StartSeq:    commit.Seq,
			StartOffset: commit.StartOffset,
		}
		p.Generations = append(p.Generations, p.CurrentGeneration)
		p.Counts.GenerationCount++
		if fact.Type == FactContextRenewed {
			state.ContextUsage = nil
			p.ContextUsage = nil
		}
	case FactUsageRecorded:
		if fact.Usage != nil {
			p.TokenUsage.Add(*fact.Usage)
		}
		if fact.ContextUsage != nil {
			usage := fact.ContextUsage
			state.ContextUsage = cloneContextUsage(usage)
			percentage := float64(0)
			if usage.ContextWindow > 0 {
				percentage = float64(usage.TotalTokens) * 100 / float64(usage.ContextWindow)
			}
			p.ContextUsage = &ContextProjection{
				ContextWindow: usage.ContextWindow,
				CurrentTokens: usage.TotalTokens,
				Percentage:    percentage,
				CalibratedAt:  &commit.At,
			}
		}
	case FactProjectionCheck:
		if fact.Checkpoint == nil || fact.Checkpoint.Projection.ThreadID != threadID ||
			fact.Checkpoint.Projection.Journal.ProjectedSeq+1 != commit.Seq ||
			fact.Checkpoint.Projection.Journal.ProjectedOffset != commit.StartOffset {
			return fmt.Errorf("%w: checkpoint does not describe its Journal prefix", ErrInvalidTransition)
		}
		p.Journal.LastCheckpointSeq = commit.Seq
		p.Journal.LastCheckpointOffset = commit.StartOffset
	}
	return nil
}

func compactProviderMessages(summary llm.Message, history []llm.Message) []llm.Message {
	provider := []llm.Message{summary}
	if summary.Compaction == nil || len(summary.Compaction.RetainedMessageIDs) == 0 {
		return provider
	}
	retained := make(map[string]struct{}, len(summary.Compaction.RetainedMessageIDs))
	for _, id := range summary.Compaction.RetainedMessageIDs {
		retained[id] = struct{}{}
	}
	for _, message := range history {
		if _, ok := retained[message.ID]; ok {
			provider = append(provider, message)
		}
	}
	return provider
}

func inputRecordPending(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var record struct {
		State string `json:"state"`
	}
	return json.Unmarshal(raw, &record) == nil && record.State == "pending"
}

func requireOpenInput(state *ReplayState, id string) (*InputProjection, error) {
	input := state.Inputs[id]
	if input == nil {
		return nil, fmt.Errorf("%w: input %q was not accepted", ErrInvalidTransition, id)
	}
	if input.State.Terminal() {
		return nil, fmt.Errorf("%w: input %q is terminal", ErrInvalidTransition, id)
	}
	return input, nil
}

func requireRunningAttempt(state *ReplayState, inputID, attemptID string) (*InputProjection, *AttemptProjection, error) {
	input, err := requireOpenInput(state, inputID)
	if err != nil {
		return nil, nil, err
	}
	for i := range input.Attempts {
		attempt := &input.Attempts[i]
		if attempt.ID == attemptID {
			if attempt.State != "running" {
				return nil, nil, fmt.Errorf("%w: attempt %q is not running", ErrInvalidTransition, attemptID)
			}
			return input, attempt, nil
		}
	}
	return nil, nil, fmt.Errorf("%w: attempt %q does not exist", ErrInvalidTransition, attemptID)
}
