package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/provenance"
	"github.com/juex-ai/juex/internal/framework/runtime/contextbudget"
)

type compactionSummaryState = contextbudget.SummaryState

type compactionSummaryToolBudget = contextbudget.SummaryToolBudget

func buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, state compactionSummaryState, policy compactionPolicy, instructions string) (string, []llm.Message) {
	system, history := contextbudget.BuildCompactionSummaryRequest(base, previous, input, state, policy, instructions)
	for index := range history {
		if history[index].ID == "" {
			history[index].ID = stableProvenanceMessageID("compaction-input-", index, history[index])
		}
	}
	return system, history
}

func buildProvenanceBoundedCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, state compactionSummaryState, policy compactionPolicy, instructions string) (string, []llm.Message, error) {
	constraint := func(message llm.Message) error {
		message.ID = stableProvenanceMessageID("compaction-input-", 0, message)
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if len(raw) > provenance.MaxInlineSnapshotBytes {
			return fmt.Errorf("derived message is %d bytes, limit is %d", len(raw), provenance.MaxInlineSnapshotBytes)
		}
		return nil
	}
	system, history, err := contextbudget.BuildCompactionSummaryRequestWithConstraint(base, previous, input, state, policy, instructions, constraint)
	if err != nil {
		return system, nil, fmt.Errorf("cannot fit compaction summary snapshot: %w", err)
	}
	for index := range history {
		history[index].ID = stableProvenanceMessageID("compaction-input-", index, history[index])
	}
	return system, history, nil
}

func buildCompactionSummaryBody(previous llm.Message, input []llm.Message, state compactionSummaryState, toolBudget compactionSummaryToolBudget, omitted int) string {
	return contextbudget.BuildCompactionSummaryBody(previous, input, state, toolBudget, omitted)
}

func compactionSummaryRequestTokenLimit(policy compactionPolicy) int {
	return contextbudget.CompactionSummaryRequestTokenLimit(policy)
}

func fitCompactionSummaryInput(sys string, previous llm.Message, input []llm.Message, state compactionSummaryState, policy compactionPolicy, limit int) ([]llm.Message, int, compactionSummaryToolBudget) {
	return contextbudget.FitCompactionSummaryInput(sys, previous, input, state, policy, limit)
}

func compactionSummaryFits(sys string, previous llm.Message, input []llm.Message, state compactionSummaryState, toolBudget compactionSummaryToolBudget, omitted, limit int) bool {
	return contextbudget.CompactionSummaryFits(sys, previous, input, state, toolBudget, omitted, limit)
}

func (e *Engine) compactionSummaryStateLocked(ctx context.Context, policy compactionPolicy) (compactionSummaryState, error) {
	contributions, err := runtimemodule.CollectCompactionContributions(ctx, runtimemodule.CompactionBudget{
		MaxBytes:       provenance.MaxInlineSnapshotBytes,
		MaxTokens:      policy.SummaryRequestTokens,
		EstimateTokens: contextbudget.EstimateTextTokens,
	}, e.policySets()...)
	return compactionSummaryState{Contributions: contributions}, err
}
