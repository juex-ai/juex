package thread

import (
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func replay(threadID string, commits []scannedCommit) (ReplayState, error) {
	state := ReplayState{}
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
	state.Projection.LastActivityAt = commit.At
	state.Projection.UpdatedAt = commit.At
	state.Projection.EventCursor.GenerationID = commit.GenerationID
	state.Projection.EventCursor.Seq = commit.Seq
	state.Projection.EventCursor.Offset = commit.EndOffset
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
			ID: InitialGeneration, Ordinal: 1, BoundarySeq: commit.Seq,
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
		applyGenerationSeed(state, *fact.Seed)
		state.Activities = []Activity{activity}
		if fact.Type == FactContextCompacted {
			state.CompactionCount++
		}
		p.CurrentGeneration = GenerationProjection{
			ID: fact.ToGenerationID, Ordinal: nextOrdinal, BoundarySeq: commit.Seq,
		}
		p.Generations = append(p.Generations, p.CurrentGeneration)
		p.Counts.GenerationCount++
		if state.ContextUsage == nil {
			p.ContextUsage = nil
		} else {
			p.ContextUsage = contextProjection(state.ContextUsage, commit.At)
		}
	case FactUsageRecorded:
		if fact.Usage != nil {
			p.TokenUsage.Add(*fact.Usage)
		}
		if fact.ContextUsage != nil {
			usage := fact.ContextUsage
			state.ContextUsage = cloneContextUsage(usage)
			p.ContextUsage = contextProjection(usage, commit.At)
		}
	}
	return nil
}

func applyGenerationSeed(state *ReplayState, seed GenerationSeed) {
	state.Messages = nil
	state.ProviderMessages = append([]llm.Message(nil), seed.ProviderMessages...)
	state.Events = append([]events.Event(nil), seed.RecoveryEvents...)
	state.CompactionCount = seed.CompactionCount
	state.ContextUsage = cloneContextUsage(seed.ContextUsage)
}

func contextProjection(usage *llm.ContextUsage, at Timestamp) *ContextProjection {
	if usage == nil {
		return nil
	}
	percentage := float64(0)
	if usage.ContextWindow > 0 {
		percentage = float64(usage.TotalTokens) * 100 / float64(usage.ContextWindow)
	}
	return &ContextProjection{
		ContextWindow: usage.ContextWindow,
		CurrentTokens: usage.TotalTokens,
		Percentage:    percentage,
		CalibratedAt:  &at,
	}
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
