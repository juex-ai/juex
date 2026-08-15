package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
)

var errEmptyCompactionSummary = errors.New("empty summary")

type compactionSummaryJournalError struct{ err error }

func (e *compactionSummaryJournalError) Error() string { return e.err.Error() }
func (e *compactionSummaryJournalError) Unwrap() error { return e.err }

func isCompactionSummaryJournalError(err error) bool {
	var target *compactionSummaryJournalError
	return errors.As(err, &target)
}

type compactionSummaryGeneration struct {
	Response llm.Response
	Provider llm.Provider
	Summary  string
	Usage    llm.Usage
	Epoch    provenance.RequestEpoch
}

func (e *Engine) generateCompactionSummaryLocked(
	ctx context.Context,
	turnID string,
	baseSystem string,
	previous llm.Message,
	input []llm.Message,
	state compactionSummaryState,
	policy compactionPolicy,
	instructions string,
) (compactionSummaryGeneration, error) {
	provider := e.compactionSummaryProviderLocked()
	maxOutputTokens := policy.SummaryMaxTokens
	summarySystem, summaryHistory := buildCompactionSummaryRequest(baseSystem, previous, input, state, policy, instructions)
	attempt := 1
	resp, epoch, err := e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, maxOutputTokens, attempt)
	var usage llm.Usage
	if isCompactionSummaryJournalError(err) {
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
	}
	if err == nil {
		usage.Add(resp.Usage)
		if summary, ok := completeCompactionSummaryText(resp); ok {
			return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
		}

		retryReason := compactionSummaryRetryReason(resp)
		retryMaxOutputTokens := e.compactionSummaryRetryMaxOutputTokens(baseSystem, previous, input, state, policy, instructions)
		if emitErr := e.emit(events.Event{Type: "context.compact.summary_retry", TurnID: turnID, Payload: ContextCompactSummaryRetryPayload{
			Attempt:                 2,
			Reason:                  retryReason,
			StopReason:              resp.StopReason,
			ReasoningOnly:           compactionResponseReasoningOnly(resp.Message),
			PreviousMaxOutputTokens: maxOutputTokens,
			MaxOutputTokens:         retryMaxOutputTokens,
			EpochID:                 epoch.EpochID,
			RequestDigest:           epoch.RequestDigest,
		}}); emitErr != nil {
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, fmt.Errorf("commit compaction summary retry: %w", emitErr)
		}
		retryPolicy := policy
		retryPolicy.SummaryMaxTokens = retryMaxOutputTokens
		summarySystem, summaryHistory = buildCompactionSummaryRequest(baseSystem, previous, input, state, retryPolicy, instructions)
		maxOutputTokens = retryMaxOutputTokens
		attempt++
		resp, epoch, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, maxOutputTokens, attempt)
		if isCompactionSummaryJournalError(err) {
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
		}
		if err == nil {
			usage.Add(resp.Usage)
			if summary, ok := completeCompactionSummaryText(resp); ok {
				return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
			}
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, ctxErr
	}
	if e.Provider != nil && provider != e.Provider {
		if emitErr := e.emit(events.Event{Type: "context.compact.summary_model_fallback", TurnID: turnID, Payload: ContextCompactSummaryFallbackPayload{
			ConfiguredModel: policy.SummaryModel,
			FallbackModel:   e.Provider.Name(),
			Error:           compactionSummaryFailure(resp, err),
			EpochID:         epoch.EpochID,
			RequestDigest:   epoch.RequestDigest,
		}}); emitErr != nil {
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, fmt.Errorf("commit compaction summary model fallback: %w", emitErr)
		}
		provider = e.Provider
		attempt++
		resp, epoch, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, maxOutputTokens, attempt)
		if isCompactionSummaryJournalError(err) {
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
		}
		if err == nil {
			usage.Add(resp.Usage)
			if summary, ok := completeCompactionSummaryText(resp); ok {
				return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
			}
		}
	}

	if err != nil {
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
	}
	if resp.StopReason == llm.StopMaxTokens && compactionSummaryText(resp) != "" {
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, fmt.Errorf("truncated summary (stop_reason=%s)", resp.StopReason)
	}
	return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, errEmptyCompactionSummary
}

func (e *Engine) completeCompactionSummary(
	ctx context.Context,
	turnID string,
	provider llm.Provider,
	system string,
	history []llm.Message,
	maxOutputTokens int,
	attempt int,
) (llm.Response, provenance.RequestEpoch, error) {
	cachePolicy := e.cachePolicyLocked()
	descriptor := e.providerProvenanceLocked(provider)
	epoch, err := e.checkpointProviderRequestEpochLocked(turnID, 0, attempt, provenance.RequestInput{
		Purpose:         "compaction",
		Provider:        descriptor,
		ContextWindow:   e.ContextWindow,
		MaxOutputTokens: maxOutputTokens,
		CachePolicy:     provenance.SafeCachePolicyFrom(cachePolicy),
		SystemPrompt:    system,
		History:         history,
	})
	if err != nil {
		return llm.Response{}, provenance.RequestEpoch{}, &compactionSummaryJournalError{err: err}
	}
	if err := e.emit(events.Event{Type: "llm.requested", TurnID: turnID, Payload: LLMRequestedPayload{
		Iter:          0,
		Purpose:       "compaction",
		HistoryLen:    len(history),
		ToolCount:     0,
		Model:         descriptor.Model,
		EpochID:       epoch.EpochID,
		RequestDigest: epoch.RequestDigest,
	}}); err != nil {
		return llm.Response{}, epoch, &compactionSummaryJournalError{err: fmt.Errorf("commit compaction provider request: %w", err)}
	}
	resp, requestErr := llm.CompleteWithOptions(ctx, provider, system, history, nil, llm.CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: maxOutputTokens,
		CachePolicy:     cachePolicy,
		RetryObserver:   e.providerRetryObserverForEpochLocked(turnID, "compaction", nil, epoch.EpochID, epoch.RequestDigest),
	})
	model := descriptor.Model
	if model == "" && provider != nil {
		model = provider.Name()
	}
	if requestErr != nil {
		emitErr := e.emit(events.Event{Type: "context.compact.summary_errored", TurnID: turnID, Payload: ContextCompactSummaryErroredPayload{
			Attempt: attempt, Model: model, Error: requestErr.Error(), EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
		}})
		if emitErr != nil {
			return resp, epoch, &compactionSummaryJournalError{err: errors.Join(requestErr, fmt.Errorf("commit compaction summary error: %w", emitErr))}
		}
		return resp, epoch, requestErr
	}
	if err := e.emit(events.Event{Type: "context.compact.summary_responded", TurnID: turnID, Payload: ContextCompactSummaryRespondedPayload{
		Attempt: attempt, Model: model, StopReason: resp.StopReason, Usage: resp.Usage, EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
	}}); err != nil {
		return resp, epoch, &compactionSummaryJournalError{err: fmt.Errorf("commit compaction summary response: %w", err)}
	}
	return resp, epoch, nil
}

func compactionSummaryText(resp llm.Response) string {
	return strings.TrimSpace(responseText(resp.Message))
}

func completeCompactionSummaryText(resp llm.Response) (string, bool) {
	summary := compactionSummaryText(resp)
	return summary, summary != "" && resp.StopReason != llm.StopMaxTokens
}

func compactionSummaryRetryReason(resp llm.Response) string {
	if compactionSummaryText(resp) != "" && resp.StopReason == llm.StopMaxTokens {
		return "max_tokens"
	}
	return "empty_summary"
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
	if resp.StopReason == llm.StopMaxTokens && compactionSummaryText(resp) != "" {
		return fmt.Sprintf("truncated summary (stop_reason=%s)", resp.StopReason)
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

func (e *Engine) compactionSummaryRetryMaxOutputTokens(
	baseSystem string,
	previous llm.Message,
	input []llm.Message,
	state compactionSummaryState,
	policy compactionPolicy,
	instructions string,
) int {
	desired := doubledSummaryMaxTokens(policy.SummaryMaxTokens)
	if desired <= 0 || policy.TriggerTokens <= 1 {
		return desired
	}

	minimumSystem, _ := buildCompactionSummaryRequest(baseSystem, previous, nil, state, policy, instructions)
	minimumBody := buildCompactionSummaryBody(previous, nil, state, policy.ToolResultMaxChars, len(input))
	minimumHistory := []llm.Message{llm.TextMessage(llm.RoleUser, minimumBody)}
	maxOutputTokens := policy.TriggerTokens - estimateContextTokens(minimumSystem, nil, minimumHistory)
	if maxOutputTokens < 1 {
		maxOutputTokens = 1
	}
	if desired > maxOutputTokens {
		return maxOutputTokens
	}
	return desired
}
