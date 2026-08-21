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
	defaultContextWindow int,
	reservedModelRef string,
) (compactionSummaryGeneration, error) {
	candidates := e.compactionSummaryCandidatesLocked(policy)
	if len(candidates) == 0 {
		return compactionSummaryGeneration{}, fmt.Errorf("no compaction summary model candidates configured")
	}
	health := e.ModelHealth
	if health == nil {
		health = llm.NewModelHealth(llm.ModelHealthOptions{})
		e.ModelHealth = health
	}
	refs := make([]string, len(candidates))
	for index := range candidates {
		refs[index] = compactionSummaryCandidateRef(candidates[index])
	}
	attempted := map[string]struct{}{}
	selection, ok := acquireCompactionSummaryCandidate(health, refs, attempted, reservedModelRef)
	var skipped []llm.ModelHealthSkip
	recordCompactionSummaryHealthSkips(attempted, &skipped, selection.Skipped)
	if !ok {
		e.emitCompactionSummaryHealthSkips(turnID, "", selection.Skipped)
		return compactionSummaryGeneration{}, modelChainError(nil, skipped)
	}
	candidate := candidates[selection.Index]
	attempted[compactionSummaryCandidateRef(candidate)] = struct{}{}
	e.emitCompactionSummaryHealthSkips(turnID, compactionSummaryCandidateRef(candidate), selection.Skipped)
	ticket := selection.Ticket
	provider := candidate.Provider
	var failures []modelAttemptFailure
	useRetryBudget := false
	candidatePolicy, contextWindow := e.compactionSummaryPolicyForCandidateLocked(candidate, defaultContextWindow)
	candidatePolicy.SummaryMaxTokens = e.compactionSummaryInitialMaxOutputTokens(baseSystem, previous, input, state, candidatePolicy, instructions)
	maxOutputTokens := candidatePolicy.SummaryMaxTokens
	summarySystem, summaryHistory := buildCompactionSummaryRequest(baseSystem, previous, input, state, candidatePolicy, instructions)
	attempt := 1
	resp, epoch, err := e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, contextWindow, maxOutputTokens, attempt)
	var usage llm.Usage
	usage.Add(resp.Usage)
	if isCompactionSummaryJournalError(err) {
		health.Complete(ticket, llm.ModelHealthNeutral, "")
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
	}
	if err == nil {
		if summary, ok := completeCompactionSummaryText(resp); ok {
			health.Complete(ticket, llm.ModelHealthSuccess, "")
			return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
		}

		retryReason := compactionSummaryRetryReason(resp)
		retryMaxOutputTokens := e.compactionSummaryRetryMaxOutputTokens(baseSystem, previous, input, state, candidatePolicy, instructions)
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
			health.Complete(ticket, llm.ModelHealthNeutral, "")
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, fmt.Errorf("commit compaction summary retry: %w", emitErr)
		}
		retryPolicy := candidatePolicy
		retryPolicy.SummaryMaxTokens = retryMaxOutputTokens
		useRetryBudget = true
		summarySystem, summaryHistory = buildCompactionSummaryRequest(baseSystem, previous, input, state, retryPolicy, instructions)
		maxOutputTokens = retryMaxOutputTokens
		attempt++
		resp, epoch, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, contextWindow, maxOutputTokens, attempt)
		usage.Add(resp.Usage)
		if isCompactionSummaryJournalError(err) {
			health.Complete(ticket, llm.ModelHealthNeutral, "")
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
		}
		if err == nil {
			if summary, ok := completeCompactionSummaryText(resp); ok {
				health.Complete(ticket, llm.ModelHealthSuccess, "")
				return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
			}
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		health.Complete(ticket, llm.ModelHealthNeutral, "")
		return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, ctxErr
	}
	for {
		failedCandidate := candidate
		failureErr := compactionSummaryAttemptError(resp, err)
		completeCompactionSummaryModelHealth(health, ticket)
		failures = append(failures, modelAttemptFailure{
			ref: compactionSummaryCandidateRef(failedCandidate),
			err: failureErr,
		})
		selection, ok = acquireCompactionSummaryCandidate(health, refs, attempted, reservedModelRef)
		recordCompactionSummaryHealthSkips(attempted, &skipped, selection.Skipped)
		fallbackRef := ""
		if ok {
			fallbackRef = refs[selection.Index]
		}
		if emitErr := e.emit(events.Event{Type: "context.compact.summary_model_fallback", TurnID: turnID, Payload: ContextCompactSummaryFallbackPayload{
			ConfiguredModel: compactionSummaryCandidateRef(failedCandidate),
			FallbackModel:   fallbackRef,
			Error:           compactionSummaryFailure(resp, err),
			EpochID:         epoch.EpochID,
			RequestDigest:   epoch.RequestDigest,
		}}); emitErr != nil {
			if ok {
				health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			}
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, fmt.Errorf("commit compaction summary model fallback: %w", emitErr)
		}
		e.emitCompactionSummaryHealthSkips(turnID, fallbackRef, selection.Skipped)
		if !ok {
			if len(failures) == 1 && len(skipped) == 0 {
				return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, failureErr
			}
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, modelChainError(failures, skipped)
		}
		nextCandidate := candidates[selection.Index]
		attempted[fallbackRef] = struct{}{}
		candidatePolicy, contextWindow = e.compactionSummaryPolicyForCandidateLocked(nextCandidate, defaultContextWindow)
		candidatePolicy.SummaryMaxTokens = e.compactionSummaryInitialMaxOutputTokens(baseSystem, previous, input, state, candidatePolicy, instructions)
		if useRetryBudget {
			candidatePolicy.SummaryMaxTokens = e.compactionSummaryRetryMaxOutputTokens(baseSystem, previous, input, state, candidatePolicy, instructions)
		}
		maxOutputTokens = candidatePolicy.SummaryMaxTokens
		summarySystem, summaryHistory = buildCompactionSummaryRequest(baseSystem, previous, input, state, candidatePolicy, instructions)
		candidate = nextCandidate
		ticket = selection.Ticket
		provider = nextCandidate.Provider
		attempt++
		resp, epoch, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, contextWindow, maxOutputTokens, attempt)
		usage.Add(resp.Usage)
		if isCompactionSummaryJournalError(err) {
			health.Complete(ticket, llm.ModelHealthNeutral, "")
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
		}
		if err == nil {
			if summary, ok := completeCompactionSummaryText(resp); ok {
				health.Complete(ticket, llm.ModelHealthSuccess, "")
				return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
			}
			if !useRetryBudget {
				retryReason := compactionSummaryRetryReason(resp)
				retryMaxOutputTokens := e.compactionSummaryRetryMaxOutputTokens(baseSystem, previous, input, state, candidatePolicy, instructions)
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
					health.Complete(ticket, llm.ModelHealthNeutral, "")
					return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, fmt.Errorf("commit compaction summary retry: %w", emitErr)
				}
				retryPolicy := candidatePolicy
				retryPolicy.SummaryMaxTokens = retryMaxOutputTokens
				useRetryBudget = true
				summarySystem, summaryHistory = buildCompactionSummaryRequest(baseSystem, previous, input, state, retryPolicy, instructions)
				maxOutputTokens = retryMaxOutputTokens
				attempt++
				resp, epoch, err = e.completeCompactionSummary(ctx, turnID, provider, summarySystem, summaryHistory, contextWindow, maxOutputTokens, attempt)
				usage.Add(resp.Usage)
				if isCompactionSummaryJournalError(err) {
					health.Complete(ticket, llm.ModelHealthNeutral, "")
					return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, err
				}
				if err == nil {
					if summary, ok := completeCompactionSummaryText(resp); ok {
						health.Complete(ticket, llm.ModelHealthSuccess, "")
						return compactionSummaryGeneration{Response: resp, Provider: provider, Summary: summary, Usage: usage, Epoch: epoch}, nil
					}
				}
			}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			health.Complete(ticket, llm.ModelHealthNeutral, "")
			return compactionSummaryGeneration{Response: resp, Provider: provider, Usage: usage, Epoch: epoch}, ctxErr
		}
	}
}

func acquireCompactionSummaryCandidate(health *llm.ModelHealth, refs []string, attempted map[string]struct{}, reservedRef string) (llm.ModelSelection, bool) {
	selection, ok := health.Acquire(refs, attempted)
	if reservedRef == "" {
		return selection, ok
	}
	if _, used := attempted[reservedRef]; used {
		return selection, ok
	}
	reservedIndex := -1
	for index, ref := range refs {
		if ref == reservedRef {
			reservedIndex = index
			break
		}
	}
	if reservedIndex < 0 || (ok && selection.Index < reservedIndex) {
		return selection, ok
	}
	if ok {
		health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
	}
	earlierSkips := make([]llm.ModelHealthSkip, 0, len(selection.Skipped))
	for _, skip := range selection.Skipped {
		index := modelRefIndex(refs, skip.Ref)
		if index >= 0 && index < reservedIndex {
			earlierSkips = append(earlierSkips, skip)
		}
	}
	return llm.ModelSelection{Index: reservedIndex, Skipped: earlierSkips}, true
}

func recordCompactionSummaryHealthSkips(attempted map[string]struct{}, all *[]llm.ModelHealthSkip, skips []llm.ModelHealthSkip) {
	for _, skip := range skips {
		attempted[skip.Ref] = struct{}{}
		*all = append(*all, skip)
	}
}

func (e *Engine) emitCompactionSummaryHealthSkips(turnID, selected string, skips []llm.ModelHealthSkip) {
	for _, skip := range skips {
		e.emitModelFallback(turnID, modelFallbackTransition{
			from:     skip.Ref,
			reason:   skip.Reason,
			cooldown: skip.CooldownRemaining,
		}, selected)
	}
}

func completeCompactionSummaryModelHealth(health *llm.ModelHealth, ticket llm.ModelAttemptTicket) {
	// Summary generation shares the serving health selector so it avoids models
	// that a normal turn has already put in cooldown. A summary-only failure must
	// not poison that serving health, though: callers may deliberately continue
	// the accepted turn after automatic compaction fails.
	health.Complete(ticket, llm.ModelHealthNeutral, "")
}

func (e *Engine) compactionSummaryPolicyForCandidateLocked(candidate ModelCandidate, defaultContextWindow int) (compactionPolicy, int) {
	contextWindow := candidateContextWindow(candidate, defaultContextWindow)
	return effectiveCompactionPolicy(e.Compaction, contextWindow), contextWindow
}

func compactionSummaryAttemptError(resp llm.Response, err error) error {
	if err != nil {
		return err
	}
	if resp.StopReason == llm.StopMaxTokens && compactionSummaryText(resp) != "" {
		return fmt.Errorf("truncated summary (stop_reason=%s)", resp.StopReason)
	}
	return errEmptyCompactionSummary
}

func (e *Engine) completeCompactionSummary(
	ctx context.Context,
	turnID string,
	provider llm.Provider,
	system string,
	history []llm.Message,
	contextWindow int,
	maxOutputTokens int,
	attempt int,
) (llm.Response, provenance.RequestEpoch, error) {
	cachePolicy := e.cachePolicyLocked()
	descriptor := e.providerProvenanceLocked(provider)
	epoch, err := e.checkpointProviderRequestEpochLocked(turnID, 0, attempt, provenance.RequestInput{
		Purpose:           "compaction",
		Provider:          descriptor,
		ContextWindow:     contextWindow,
		MaxOutputTokens:   maxOutputTokens,
		CachePolicy:       provenance.SafeCachePolicyFrom(cachePolicy),
		SystemPrompt:      system,
		SystemPromptParts: []string{system},
		History:           history,
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
	return e.compactionSummaryMaxOutputTokens(baseSystem, previous, input, state, policy, instructions, doubledSummaryMaxTokens(policy.SummaryMaxTokens))
}

func (e *Engine) compactionSummaryInitialMaxOutputTokens(
	baseSystem string,
	previous llm.Message,
	input []llm.Message,
	state compactionSummaryState,
	policy compactionPolicy,
	instructions string,
) int {
	return e.compactionSummaryMaxOutputTokens(baseSystem, previous, input, state, policy, instructions, policy.SummaryMaxTokens)
}

func (e *Engine) compactionSummaryMaxOutputTokens(
	baseSystem string,
	previous llm.Message,
	input []llm.Message,
	state compactionSummaryState,
	policy compactionPolicy,
	instructions string,
	desired int,
) int {
	if desired <= 0 {
		return desired
	}
	requestBudget := policy.SummaryRequestTokens
	if requestBudget <= 0 {
		requestBudget = policy.TriggerTokens
	}
	if requestBudget <= 1 {
		return 1
	}

	minimumSystem, _ := buildCompactionSummaryRequest(baseSystem, previous, nil, state, policy, instructions)
	minimumInput, omitted, minChars := fitCompactionSummaryInput(minimumSystem, previous, input, state, policy, 1)
	minimumBody := buildCompactionSummaryBody(previous, minimumInput, state, minChars, omitted)
	minimumHistory := []llm.Message{llm.TextMessage(llm.RoleUser, minimumBody)}
	maxOutputTokens := requestBudget - estimateContextTokens(minimumSystem, nil, minimumHistory)
	if maxOutputTokens < 1 {
		maxOutputTokens = 1
	}
	if desired > maxOutputTokens {
		return maxOutputTokens
	}
	return desired
}
