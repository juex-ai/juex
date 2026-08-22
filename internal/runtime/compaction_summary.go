package runtime

import (
	"fmt"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

type CompactionSummaryState = contextbudget.SummaryState
type CompactionSummaryGoal = contextbudget.SummaryGoal

type compactionSummaryState = CompactionSummaryState

type GoalCompactionStateProvider interface {
	GoalCompactionState() (*CompactionSummaryGoal, error)
}

type NotesCompactionStateProvider interface {
	NotesCompactionState() (string, error)
}

func buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, state compactionSummaryState, policy compactionPolicy, instructions string) (string, []llm.Message) {
	system, history := contextbudget.BuildCompactionSummaryRequest(base, previous, input, state, policy, instructions)
	for index := range history {
		if history[index].ID == "" {
			history[index].ID = stableProvenanceMessageID("compaction-input-", index, history[index])
		}
	}
	return system, history
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
	var goalProvider, notesProvider runtimemodule.ID
	modules := e.SessionRuntimeSnapshot().Modules
	if modules == nil {
		return summaryState, nil
	}
	for _, module := range modules.Modules() {
		if provider, ok := module.(GoalCompactionStateProvider); ok {
			if goalProvider != "" {
				return compactionSummaryState{}, fmt.Errorf("compaction state: goal provided by modules %q and %q", goalProvider, module.ID())
			}
			goalProvider = module.ID()
			goal, err := provider.GoalCompactionState()
			if err != nil {
				return compactionSummaryState{}, fmt.Errorf("compaction goal state from module %q: %w", module.ID(), err)
			}
			summaryState.Goal = goal
		}
		if provider, ok := module.(NotesCompactionStateProvider); ok {
			if notesProvider != "" {
				return compactionSummaryState{}, fmt.Errorf("compaction state: notes provided by modules %q and %q", notesProvider, module.ID())
			}
			notesProvider = module.ID()
			notes, err := provider.NotesCompactionState()
			if err != nil {
				return compactionSummaryState{}, fmt.Errorf("compaction notes state from module %q: %w", module.ID(), err)
			}
			summaryState.Notes = notes
		}
	}
	return summaryState, nil
}
