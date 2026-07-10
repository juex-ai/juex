package runtime

import (
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

type compactionSelection = contextbudget.Selection

func selectCompactionInput(history []llm.Message, policy compactionPolicy) compactionSelection {
	return contextbudget.SelectInputWithEstimator(history, policy, estimateMessageTokens)
}

func selectCompactionInputWithEstimator(history []llm.Message, policy compactionPolicy, estimateMessages func([]llm.Message) int) compactionSelection {
	if estimateMessages == nil {
		estimateMessages = estimateMessageTokens
	}
	return contextbudget.SelectInputWithEstimator(history, policy, estimateMessages)
}
