package runtime

import (
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

const contextUsageResponseKey = contextbudget.ContextUsageResponseKey

type tokenEstimateCalibration struct {
	inner contextbudget.TokenEstimateCalibration
}

func (c *tokenEstimateCalibration) update(realTokens, estimatedTokens int) {
	if c == nil {
		return
	}
	c.inner.Update(realTokens, estimatedTokens)
}

func (c tokenEstimateCalibration) apply(tokens int) int {
	return c.inner.Apply(tokens)
}

func contextUsageSnapshot(model string, contextWindow int, usage llm.Usage, sections []prompt.Section, tools []llm.ToolSpec, history []llm.Message) llm.ContextUsage {
	return contextbudget.ContextUsageSnapshot(model, contextWindow, DefaultContextWindowTokens, usage, sections, tools, history)
}

func (e *Engine) contextUsageSnapshot(model string, contextWindow int, usage llm.Usage, sections []prompt.Section, tools []llm.ToolSpec, history []llm.Message) llm.ContextUsage {
	snapshot := contextUsageSnapshot(model, contextWindow, usage, sections, tools, history)
	for i := range snapshot.Breakdown {
		if snapshot.Breakdown[i].Key == contextUsageResponseKey {
			continue
		}
		snapshot.Breakdown[i].Tokens = e.applyTokenEstimateCalibration(snapshot.Breakdown[i].Tokens)
	}
	if usage.InputTokens <= 0 {
		snapshot.InputTokens = estimatedInputTokens(snapshot.Breakdown)
	}
	snapshot.TotalTokens = snapshot.InputTokens + snapshot.OutputTokens
	return snapshot
}

func estimatedInputTokens(parts []llm.ContextUsagePart) int {
	return contextbudget.EstimatedInputTokens(parts)
}

func EstimateTextTokens(text string) int {
	return contextbudget.EstimateTextTokens(text)
}

func EstimateCharsAsTokens(chars int) int {
	return contextbudget.EstimateCharsAsTokens(chars)
}

func estimateMessageTokens(history []llm.Message) int {
	return contextbudget.EstimateMessageTokens(history)
}

func estimateContextTokens(systemPrompt string, tools []llm.ToolSpec, history []llm.Message) int {
	return contextbudget.EstimateContextTokens(systemPrompt, tools, history)
}

func (e *Engine) updateTokenEstimateCalibration(realTokens, estimatedTokens int) {
	if e == nil {
		return
	}
	e.tokenCalibrationMu.Lock()
	defer e.tokenCalibrationMu.Unlock()
	e.tokenCalibration.update(realTokens, estimatedTokens)
}

func (e *Engine) applyTokenEstimateCalibration(tokens int) int {
	if e == nil {
		return tokens
	}
	e.tokenCalibrationMu.RLock()
	defer e.tokenCalibrationMu.RUnlock()
	return e.tokenCalibration.apply(tokens)
}

func (e *Engine) estimateContextTokens(systemPrompt string, tools []llm.ToolSpec, history []llm.Message) int {
	return e.applyTokenEstimateCalibration(estimateContextTokens(systemPrompt, tools, history))
}

func (e *Engine) estimateMessageTokens(history []llm.Message) int {
	return e.applyTokenEstimateCalibration(estimateMessageTokens(history))
}
