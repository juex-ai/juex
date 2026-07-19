package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
	runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"
)

const DefaultContextWindowTokens = runtimepolicy.DefaultContextWindowTokens

type CompactionResult struct {
	MessageID          string `json:"message_id,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Auto               bool   `json:"auto"`
	TokensBefore       int    `json:"tokens_before,omitempty"`
	TokensAfter        int    `json:"tokens_after,omitempty"`
	SummaryChars       int    `json:"summary_chars,omitempty"`
	SummaryModel       string `json:"summary_model,omitempty"`
	TailStartMessageID string `json:"tail_start_message_id,omitempty"`
	FirstKeptMessageID string `json:"first_kept_message_id,omitempty"`
}

func (e *Engine) maybeCompact(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, incoming llm.Message) error {
	policy := effectiveCompactionPolicy(e.Compaction, e.ContextWindow)
	if !policy.Enabled {
		return nil
	}

	projected := e.activeContextLocked(incoming).Messages
	estimated := e.estimateContextTokens(systemPrompt, tools, projected)
	if estimated < policy.TriggerTokens {
		return nil
	}
	if e.autoCompactFailures >= policy.MaxAutoFailures {
		err := fmt.Errorf("auto compaction paused after %d consecutive failures; run /compact with focus instructions or start a new session", policy.MaxAutoFailures)
		e.emit(events.Event{Type: "context.compact.skipped", TurnID: turnID, Payload: ContextCompactSkippedPayload{
			Reason:              "failure_circuit_breaker",
			Auto:                true,
			ConsecutiveFailures: e.autoCompactFailures,
			MaxAutoFailures:     policy.MaxAutoFailures,
			Error:               err.Error(),
		}})
		return err
	}

	_, err := e.compactLocked(ctx, turnID, systemPrompt, tools, "auto", true, "")
	if err != nil {
		e.autoCompactFailures++
		return err
	}
	e.autoCompactFailures = 0
	return err
}

func (e *Engine) Compact(ctx context.Context, turnID, systemPrompt, reason string, auto bool) (CompactionResult, error) {
	return e.CompactWithInstructions(ctx, turnID, systemPrompt, reason, auto, "")
}

func (e *Engine) CompactWithInstructions(ctx context.Context, turnID, systemPrompt, reason string, auto bool, instructions string) (CompactionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compactLocked(ctx, turnID, systemPrompt, e.compactionToolsLocked(), reason, auto, instructions)
}

func (e *Engine) compactLocked(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string) (CompactionResult, error) {
	return e.compactLockedForContextWindow(ctx, turnID, systemPrompt, tools, reason, auto, instructions, e.ContextWindow)
}

func (e *Engine) compactLockedForContextWindow(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string, contextWindow int) (CompactionResult, error) {
	policy := effectiveCompactionPolicy(e.Compaction, contextWindow)
	if !policy.Enabled {
		return CompactionResult{}, nil
	}
	selection := ensureCompactionProgress(selectCompactionInputWithEstimator(providerVisibleMessages(e.Session.History), policy, e.estimateMessageTokens))
	if len(selection.SummaryInput) == 0 && !selection.HasPreviousSummary {
		return CompactionResult{}, nil
	}
	summaryState, err := e.compactionSummaryStateLocked()
	if err != nil {
		compactErr := fmt.Errorf("compact context: %w", err)
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: ContextCompactErroredPayload{
			Reason: reason,
			Auto:   auto,
			Error:  compactErr.Error(),
		}})
		return CompactionResult{}, compactErr
	}
	preReq := e.newHookRequest(hooks.EventPreCompact, turnID)
	preReq.CompactReason = reason
	preReq.CompactAuto = auto
	preResults, err := e.runHooks(ctx, preReq)
	if err != nil {
		compactErr := fmt.Errorf("compact context: %w", err)
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: ContextCompactErroredPayload{
			Reason: reason,
			Auto:   auto,
			Error:  compactErr.Error(),
		}})
		return CompactionResult{}, compactErr
	}
	instructions = mergeCompactInstructions(policy.Instructions, instructions)
	instructions = appendCompactHookInstructions(instructions, preResults)

	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	tokensBefore := e.estimateContextTokens(systemPrompt, tools, e.activeContextLocked().Messages)
	e.emit(events.Event{Type: "context.compact.started", TurnID: turnID, Payload: ContextCompactStartedPayload{
		Reason:           reason,
		Auto:             auto,
		EstimatedTokens:  tokensBefore,
		TokensBefore:     tokensBefore,
		ContextWindow:    contextWindow,
		ReserveTokens:    policy.ReserveTokens,
		KeepRecentTokens: policy.KeepRecentTokens,
		TailTurns:        policy.TailTurns,
	}})

	generation, err := e.generateCompactionSummaryLocked(ctx, turnID, systemPrompt, selection.PreviousSummary, selection.SummaryInput, summaryState, policy, instructions)
	if err != nil {
		e.Session.RecordResponseUsage(generation.Usage, nil)
		compactErr := fmt.Errorf("compact context: %w", err)
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: ContextCompactErroredPayload{
			Reason: reason,
			Auto:   auto,
			Error:  compactErr.Error(),
		}})
		return CompactionResult{}, compactErr
	}
	resp := generation.Response
	summaryProvider := generation.Provider
	summary := generation.Summary

	model := resp.Message.Model
	if model == "" && summaryProvider != nil {
		model = summaryProvider.Name()
	}
	msg := llm.TextMessage(llm.RoleUser, compactMessageText(summary))
	msg.Kind = llm.MessageKindCompact
	msg.Compaction = &llm.CompactionMetadata{
		Auto:               auto,
		Reason:             reason,
		FirstKeptMessageID: selection.FirstKeptMessageID,
		TailStartMessageID: selection.TailStartMessageID,
		TokensBefore:       tokensBefore,
		SummaryChars:       len(summary),
		SummaryModel:       model,
	}
	if selection.HasPreviousSummary {
		msg.Compaction.PreviousSummaryID = selection.PreviousSummary.ID
	}
	simulated := make([]llm.Message, 0, len(e.Session.History)+1)
	simulated = append(simulated, e.Session.History...)
	simulated = append(simulated, msg)
	tokensAfter := e.estimateContextTokens(systemPrompt, tools, assembleActiveContext(simulated, nil).Messages)
	msg.Compaction.TokensAfter = tokensAfter
	if err := e.Session.Append(msg); err != nil {
		err := fmt.Errorf("session append compact: %w", err)
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: ContextCompactErroredPayload{
			Reason: reason,
			Auto:   auto,
			Error:  err.Error(),
		}})
		return CompactionResult{}, err
	}
	e.autoCompactFailures = 0
	if len(e.Session.History) > 0 {
		msg = e.Session.History[len(e.Session.History)-1]
	}
	result := CompactionResult{
		MessageID:          msg.ID,
		Reason:             reason,
		Auto:               auto,
		TokensBefore:       tokensBefore,
		TokensAfter:        tokensAfter,
		SummaryChars:       len(summary),
		SummaryModel:       model,
		TailStartMessageID: selection.TailStartMessageID,
		FirstKeptMessageID: selection.FirstKeptMessageID,
	}
	contextUsage := llm.ContextUsage{
		Model:         model,
		ContextWindow: contextWindow,
		InputTokens:   tokensAfter,
		TotalTokens:   tokensAfter,
		Breakdown: []llm.ContextUsagePart{
			{Key: "active_context", Label: "active context after compaction", Tokens: tokensAfter},
		},
	}
	e.Session.RecordResponseUsage(generation.Usage, &contextUsage)
	postReq := e.newHookRequest(hooks.EventPostCompact, turnID)
	postReq.CompactReason = reason
	postReq.CompactAuto = auto
	postResults, err := e.runHooks(ctx, postReq)
	if err != nil {
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: ContextCompactErroredPayload{
			Reason: reason,
			Auto:   auto,
			Error:  fmt.Sprintf("compact context: post hook failed: %v", err),
		}})
		return result, nil
	}
	e.queueHookRuntimeContext(postResults)
	e.emit(events.Event{Type: "context.compact.completed", TurnID: turnID, Payload: ContextCompactCompletedPayload{
		MessageID:          result.MessageID,
		Reason:             result.Reason,
		Auto:               result.Auto,
		EstimatedTokens:    result.TokensBefore,
		TokensBefore:       result.TokensBefore,
		TokensAfter:        result.TokensAfter,
		SummaryChars:       result.SummaryChars,
		SummaryModel:       result.SummaryModel,
		TailStartMessageID: result.TailStartMessageID,
		ContextWindow:      contextWindow,
		ReserveTokens:      policy.ReserveTokens,
		KeepRecentTokens:   policy.KeepRecentTokens,
		ContextUsage:       &contextUsage,
	}})
	return result, nil
}

func mergeCompactInstructions(parts ...string) string {
	merged := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			merged = append(merged, part)
		}
	}
	return strings.Join(merged, "\n\n")
}

func compactMessageText(summary string) string {
	return "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\n" + summary
}

func (e *Engine) compactionToolsLocked() []llm.ToolSpec {
	if e == nil || e.Tools == nil {
		return nil
	}
	return e.Tools.Specs()
}

func (e *Engine) compactionSummaryProviderLocked() llm.Provider {
	if e != nil && e.SummaryProvider != nil {
		return e.SummaryProvider
	}
	if e != nil {
		return e.Provider
	}
	return nil
}

func ensureCompactionProgress(sel compactionSelection) compactionSelection {
	if len(sel.SummaryInput) > 0 || len(sel.RetainedTail) == 0 {
		return sel
	}
	keepStart := len(sel.RetainedTail) - 1
	if keepStart < 0 {
		keepStart = 0
	}
	for keepStart > 0 && contextbudget.StartsWithToolResult(sel.RetainedTail[keepStart]) {
		keepStart--
	}
	sel.SummaryInput = append(sel.SummaryInput, sel.RetainedTail[:keepStart]...)
	if keepStart == 0 {
		sel.SummaryInput = append(sel.SummaryInput, sel.RetainedTail...)
		sel.RetainedTail = nil
		sel.FirstKeptMessageID = ""
		sel.TailStartMessageID = ""
		return sel
	}
	sel.RetainedTail = append([]llm.Message(nil), sel.RetainedTail[keepStart:]...)
	sel.FirstKeptMessageID = sel.RetainedTail[0].ID
	sel.TailStartMessageID = sel.RetainedTail[0].ID
	return sel
}
