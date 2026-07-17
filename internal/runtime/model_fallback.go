package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

type modelAttemptFailure struct {
	ref string
	err error
}

type modelFallbackTransition struct {
	from     string
	reason   string
	cooldown time.Duration
	probe    bool
}

type modelRequestError struct {
	err           error
	contextWindow int
}

func (e *modelRequestError) Error() string { return e.err.Error() }
func (e *modelRequestError) Unwrap() error { return e.err }

func (e *Engine) effectiveModelCandidatesLocked() []ModelCandidate {
	if len(e.ModelCandidates) > 0 {
		out := make([]ModelCandidate, 0, len(e.ModelCandidates))
		seen := map[string]struct{}{}
		for _, candidate := range e.ModelCandidates {
			if candidate.Provider == nil {
				continue
			}
			if candidate.Ref == "" {
				candidate.Ref = candidate.Provider.Name()
			}
			if _, duplicate := seen[candidate.Ref]; duplicate {
				continue
			}
			seen[candidate.Ref] = struct{}{}
			out = append(out, candidate)
		}
		return out
	}
	if e.Provider == nil {
		return nil
	}
	return []ModelCandidate{{
		Ref:             e.Provider.Name(),
		Provider:        e.Provider,
		ContextWindow:   e.ContextWindow,
		MaxOutputTokens: e.MaxOutputTokens,
	}}
}

func candidateContextWindow(candidate ModelCandidate, fallback int) int {
	if candidate.ContextWindow > 0 {
		return candidate.ContextWindow
	}
	if fallback > 0 {
		return fallback
	}
	return DefaultContextWindowTokens
}

func candidateMaxOutputTokens(candidate ModelCandidate, fallback int) int {
	if candidate.MaxOutputTokens > 0 {
		return candidate.MaxOutputTokens
	}
	return fallback
}

func (e *Engine) prepareCandidateRequestLocked(ctx context.Context, turnID string, prepared preparedTurnContext, base providerTurnRequest, candidate ModelCandidate, notice *llm.Message, allowPreflightCompaction bool) (providerTurnRequest, error) {
	if err := cancellation.ContextError(ctx); err != nil {
		return base, err
	}
	contextWindow := candidateContextWindow(candidate, e.ContextWindow)
	policy := effectiveCompactionPolicy(e.Compaction, contextWindow)
	request, err := e.projectCandidateHistoryLocked(turnID, prepared, base, policy, notice)
	if err != nil {
		return base, err
	}
	if allowPreflightCompaction && policy.Enabled && request.estimatedInputTokens >= policy.TriggerTokens {
		if _, err := e.compactLockedForContextWindow(ctx, turnID, prepared.systemPrompt, prepared.tools, "model_fallback_preflight", true, "", contextWindow); err != nil {
			return base, err
		}
		base.hookContext = e.pendingHookRuntimeContextSnapshot()
		base.hookContextCount = len(base.hookContext)
		base.history = e.activeContextLockedWithHookContext(base.hookContext).Messages
		if err := cancellation.ContextError(ctx); err != nil {
			return base, err
		}
		request, err := e.projectCandidateHistoryLocked(turnID, prepared, base, policy, notice)
		if err != nil {
			return base, err
		}
		return request, nil
	}
	return request, nil
}

func (e *Engine) projectCandidateHistoryLocked(turnID string, prepared preparedTurnContext, base providerTurnRequest, policy compactionPolicy, notice *llm.Message) (providerTurnRequest, error) {
	projected, projection, err := e.projectMessagesForProviderLocked(base.history, policy)
	if err != nil {
		return providerTurnRequest{}, err
	}
	e.emitProjectionApplied(turnID, projection)
	projected, projection = stripRedactedReasoningForProviderBudget(prepared.systemPrompt, prepared.tools, projected, policy)
	e.emitProjectionApplied(turnID, projection)
	if notice != nil {
		projected = append(projected, *notice)
	}
	base.history = projected
	base.estimatedInputTokens = estimateContextTokens(prepared.systemPrompt, prepared.tools, projected)
	return base, nil
}

func previousAssistantModel(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == llm.RoleAssistant && history[i].Model != "" {
			return history[i].Model
		}
	}
	return ""
}

func modelSwitchNotice(previous, selected string, chain []string, selection llm.ModelSelection, pending *modelFallbackTransition, failures []modelAttemptFailure, skipped []llm.ModelHealthSkip) *llm.Message {
	previousIndex := modelRefIndex(chain, previous)
	selectedIndex := modelRefIndex(chain, selected)
	if previousIndex < 0 || selectedIndex < 0 || previousIndex == selectedIndex {
		return nil
	}
	var text string
	switch {
	case selectedIndex < previousIndex && selection.Ticket.Probe:
		text = fmt.Sprintf("<system-reminder>A higher-priority model is healthy again. You are now serving this conversation as %s instead of %s. Briefly tell the user that the model recovered and was switched back.</system-reminder>", selected, previous)
	case selectedIndex > previousIndex:
		reason := failureReasonFor(previous, failures, skipped, pending)
		if reason == "" {
			return nil
		}
		text = fmt.Sprintf("<system-reminder>The previous serving model %s became unavailable (%s). You are now serving this conversation as %s. Briefly tell the user that the model was switched so work could continue.</system-reminder>", previous, reason, selected)
	default:
		return nil
	}
	notice := llm.TextMessage(llm.RoleUser, text)
	notice.Kind = llm.MessageKindModelFallback
	return &notice
}

func failureReasonFor(ref string, failures []modelAttemptFailure, skips []llm.ModelHealthSkip, pending *modelFallbackTransition) string {
	for _, failure := range failures {
		if failure.ref == ref {
			if reason, ok := llm.ClassifyFallbackError(failure.err); ok {
				return string(reason)
			}
		}
	}
	for _, skip := range skips {
		if skip.Ref == ref {
			return skip.Reason
		}
	}
	if pending != nil && pending.from == ref {
		return pending.reason
	}
	return ""
}

func modelRefIndex(chain []string, ref string) int {
	for i := range chain {
		if chain[i] == ref {
			return i
		}
	}
	return -1
}

func (e *Engine) emitModelFallback(turnID string, transition modelFallbackTransition, to string) {
	e.emit(events.Event{Type: "llm.fallback", TurnID: turnID, Payload: LLMFallbackPayload{
		From:       transition.from,
		To:         to,
		Reason:     transition.reason,
		CooldownMS: transition.cooldown.Milliseconds(),
		Probe:      transition.probe,
	}})
}

func modelChainError(failures []modelAttemptFailure, skipped []llm.ModelHealthSkip) error {
	parts := make([]string, 0, len(failures)+len(skipped))
	for _, failure := range failures {
		parts = append(parts, fmt.Sprintf("%s: %s", failure.ref, boundedModelError(failure.err)))
	}
	for _, skip := range skipped {
		parts = append(parts, fmt.Sprintf("%s: unavailable (%s, cooldown %s)", skip.Ref, skip.Reason, skip.CooldownRemaining.Round(time.Millisecond)))
	}
	if len(parts) == 0 {
		return fmt.Errorf("llm: no model candidate available")
	}
	return fmt.Errorf("llm: model fallback chain exhausted: %s", strings.Join(parts, "; "))
}

func boundedModelError(err error) string {
	if err == nil {
		return "unknown error"
	}
	text := err.Error()
	const max = 300
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}
