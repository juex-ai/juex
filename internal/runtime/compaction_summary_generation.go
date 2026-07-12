package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

var errEmptyCompactionSummary = errors.New("empty summary")

type compactionSummaryGeneration struct {
	Response llm.Response
	Provider llm.Provider
	Summary  string
	Usage    llm.Usage
}

func (e *Engine) generateCompactionSummaryLocked(
	ctx context.Context,
	turnID string,
	baseSystem string,
	previous llm.Message,
	input []llm.Message,
	policy compactionPolicy,
	instructions string,
) (compactionSummaryGeneration, error) {
	provider := e.compactionSummaryProviderLocked()
	maxOutputTokens := policy.SummaryMaxTokens
	summarySystem, summaryHistory := buildCompactionSummaryRequest(baseSystem, previous, input, policy, instructions)
	resp, err := e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, maxOutputTokens)
	var usage llm.Usage
	if err == nil {
		usage.Add(resp.Usage)
		if summary := compactionSummaryText(resp); summary != "" {
			return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage}, nil
		}

		retryMaxOutputTokens := doubledSummaryMaxTokens(maxOutputTokens)
		e.emit(events.Event{Type: "context.compact.summary_retry", TurnID: turnID, Payload: ContextCompactSummaryRetryPayload{
			Attempt:                 2,
			Reason:                  "empty_summary",
			StopReason:              resp.StopReason,
			ReasoningOnly:           compactionResponseReasoningOnly(resp.Message),
			PreviousMaxOutputTokens: maxOutputTokens,
			MaxOutputTokens:         retryMaxOutputTokens,
		}})
		retryPolicy := policy
		retryPolicy.SummaryMaxTokens = retryMaxOutputTokens
		summarySystem, summaryHistory = buildCompactionSummaryRequest(baseSystem, previous, input, retryPolicy, instructions)
		maxOutputTokens = retryMaxOutputTokens
		resp, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, maxOutputTokens)
		if err == nil {
			usage.Add(resp.Usage)
			if summary := compactionSummaryText(resp); summary != "" {
				return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage}, nil
			}
		}
	}

	if e.Provider != nil && provider != e.Provider {
		e.emit(events.Event{Type: "context.compact.summary_model_fallback", TurnID: turnID, Payload: ContextCompactSummaryFallbackPayload{
			ConfiguredModel: policy.SummaryModel,
			FallbackModel:   e.Provider.Name(),
			Error:           compactionSummaryFailure(resp, err),
		}})
		provider = e.Provider
		resp, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, maxOutputTokens)
		if err == nil {
			usage.Add(resp.Usage)
			if summary := compactionSummaryText(resp); summary != "" {
				return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage}, nil
			}
		}
	}

	if err != nil {
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage}, err
	}
	return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage}, errEmptyCompactionSummary
}

func (e *Engine) completeCompactionSummary(
	ctx context.Context,
	turnID string,
	provider llm.Provider,
	system string,
	history []llm.Message,
	maxOutputTokens int,
) (llm.Response, error) {
	return llm.CompleteWithOptions(ctx, provider, system, history, nil, llm.CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: maxOutputTokens,
		CachePolicy:     e.cachePolicyLocked(),
		RetryObserver:   e.providerRetryObserverLocked(turnID, "compaction", nil),
	})
}

func compactionSummaryText(resp llm.Response) string {
	return strings.TrimSpace(responseText(resp.Message))
}

func compactionResponseReasoningOnly(msg llm.Message) bool {
	hasReasoning := false
	for _, block := range msg.Blocks {
		switch block.Type {
		case llm.BlockReasoning:
			hasReasoning = true
		case llm.BlockText:
			if strings.TrimSpace(block.Text) != "" {
				return false
			}
		default:
			return false
		}
	}
	return hasReasoning
}

func compactionSummaryFailure(resp llm.Response, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("empty summary (stop_reason=%s, reasoning_only=%t)", resp.StopReason, compactionResponseReasoningOnly(resp.Message))
}

func doubledSummaryMaxTokens(value int) int {
	if value <= 0 {
		return value
	}
	maxInt := int(^uint(0) >> 1)
	if value > maxInt/2 {
		return maxInt
	}
	return value * 2
}
