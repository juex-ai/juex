package runtime

import (
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

type compactionSummaryState = contextbudget.SummaryState

func buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, state compactionSummaryState, policy compactionPolicy, instructions string) (string, []llm.Message) {
	return contextbudget.BuildCompactionSummaryRequest(base, previous, input, state, policy, instructions)
}

func buildCompactionSummaryBody(previous llm.Message, input []llm.Message, state compactionSummaryState, maxChars, omitted int) string {
	return contextbudget.BuildCompactionSummaryBody(previous, input, state, maxChars, omitted)
}

func compactionSummaryRequestTokenLimit(policy compactionPolicy) int {
	return contextbudget.CompactionSummaryRequestTokenLimit(policy)
}

func fitCompactionSummaryInput(sys string, previous llm.Message, input []llm.Message, state compactionSummaryState, policy compactionPolicy, limit int) ([]llm.Message, int, int) {
	return contextbudget.FitCompactionSummaryInput(sys, previous, input, state, policy, limit)
}

func compactionSummaryFits(sys string, previous llm.Message, input []llm.Message, state compactionSummaryState, maxChars, omitted, limit int) bool {
	return contextbudget.CompactionSummaryFits(sys, previous, input, state, maxChars, omitted, limit)
}

func (e *Engine) compactionSummaryStateLocked() (compactionSummaryState, error) {
	var summaryState compactionSummaryState
	if store := e.goalStateStoreLocked(); store != nil {
		state, err := store.Snapshot()
		if err != nil {
			return compactionSummaryState{}, fmt.Errorf("compaction goal state: %w", err)
		}
		if rendered, ok := state.RenderProviderContext(); ok {
			summaryState.GoalContract = rendered
		}
	}
	if store := e.notesStoreLocked(); store != nil {
		snapshot, err := store.Snapshot()
		if err != nil {
			return compactionSummaryState{}, fmt.Errorf("compaction notes: %w", err)
		}
		summaryState.Notes = snapshot.Content
	}
	return summaryState, nil
}
