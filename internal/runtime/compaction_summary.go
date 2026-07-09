package runtime

import (
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

func buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, policy compactionPolicy, instructions string) (string, []llm.Message) {
	return contextbudget.BuildCompactionSummaryRequest(base, previous, input, policy, instructions)
}

func buildCompactionSummaryBody(previous llm.Message, input []llm.Message, maxChars, omitted int) string {
	return contextbudget.BuildCompactionSummaryBody(previous, input, maxChars, omitted)
}

func compactionSummaryRequestTokenLimit(policy compactionPolicy) int {
	return contextbudget.CompactionSummaryRequestTokenLimit(policy)
}

func fitCompactionSummaryInput(sys string, previous llm.Message, input []llm.Message, policy compactionPolicy, limit int) ([]llm.Message, int, int) {
	return contextbudget.FitCompactionSummaryInput(sys, previous, input, policy, limit)
}

func compactionSummaryFits(sys string, previous llm.Message, input []llm.Message, maxChars, omitted, limit int) bool {
	return contextbudget.CompactionSummaryFits(sys, previous, input, maxChars, omitted, limit)
}
