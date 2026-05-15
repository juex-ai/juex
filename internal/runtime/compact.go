package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const DefaultContextWindowTokens = 256000

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
	estimated := estimateContextTokens(systemPrompt, tools, projected)
	if estimated < policy.TriggerTokens {
		return nil
	}

	_, err := e.compactLocked(ctx, turnID, systemPrompt, "auto", true)
	return err
}

func (e *Engine) Compact(ctx context.Context, turnID, systemPrompt, reason string, auto bool) (CompactionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compactLocked(ctx, turnID, systemPrompt, reason, auto)
}

func (e *Engine) compactLocked(ctx context.Context, turnID, systemPrompt, reason string, auto bool) (CompactionResult, error) {
	policy := effectiveCompactionPolicy(e.Compaction, e.ContextWindow)
	if !policy.Enabled {
		return CompactionResult{}, nil
	}
	selection := ensureCompactionProgress(selectCompactionInput(e.Session.History, policy))
	if len(selection.SummaryInput) == 0 && !selection.HasPreviousSummary {
		return CompactionResult{}, nil
	}

	contextWindow := e.ContextWindow
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	tokensBefore := estimateContextTokens(systemPrompt, nil, e.activeContextLocked().Messages)
	e.emit(events.Event{Type: "context.compact.started", TurnID: turnID, Payload: map[string]any{
		"reason":             reason,
		"auto":               auto,
		"estimated_tokens":   tokensBefore,
		"tokens_before":      tokensBefore,
		"context_window":     contextWindow,
		"reserve_tokens":     policy.ReserveTokens,
		"keep_recent_tokens": policy.KeepRecentTokens,
		"tail_turns":         policy.TailTurns,
	}})

	summarySystem, summaryHistory := buildCompactionSummaryRequest(systemPrompt, selection.PreviousSummary, selection.SummaryInput, policy)
	resp, err := llm.CompleteWithOptions(ctx, e.Provider, summarySystem, summaryHistory, nil, llm.CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: policy.SummaryMaxTokens,
		DisableThinking: true,
	})
	if err != nil {
		compactErr := fmt.Errorf("compact context: %w", err)
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: map[string]any{
			"reason": reason,
			"auto":   auto,
			"error":  compactErr.Error(),
		}})
		return CompactionResult{}, compactErr
	}
	summary := strings.TrimSpace(responseText(resp.Message))
	if summary == "" {
		err := fmt.Errorf("compact context: empty summary")
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: map[string]any{
			"reason": reason,
			"auto":   auto,
			"error":  err.Error(),
		}})
		return CompactionResult{}, err
	}

	model := resp.Message.Model
	if model == "" && e.Provider != nil {
		model = e.Provider.Name()
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
	tokensAfter := estimateContextTokens(systemPrompt, nil, assembleActiveContext(simulated, nil).Messages)
	msg.Compaction.TokensAfter = tokensAfter
	if err := e.Session.Append(msg); err != nil {
		err := fmt.Errorf("session append compact: %w", err)
		e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: map[string]any{
			"reason": reason,
			"auto":   auto,
			"error":  err.Error(),
		}})
		return CompactionResult{}, err
	}
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
	e.emit(events.Event{Type: "context.compact.completed", TurnID: turnID, Payload: map[string]any{
		"message_id":            result.MessageID,
		"reason":                result.Reason,
		"auto":                  result.Auto,
		"estimated_tokens":      result.TokensBefore,
		"tokens_before":         result.TokensBefore,
		"tokens_after":          result.TokensAfter,
		"summary_chars":         result.SummaryChars,
		"summary_model":         result.SummaryModel,
		"tail_start_message_id": result.TailStartMessageID,
		"context_window":        contextWindow,
		"reserve_tokens":        policy.ReserveTokens,
		"keep_recent_tokens":    policy.KeepRecentTokens,
	}})
	return result, nil
}

func compactMessageText(summary string) string {
	return "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\n" + summary
}

func ensureCompactionProgress(sel compactionSelection) compactionSelection {
	if len(sel.SummaryInput) > 0 || len(sel.RetainedTail) == 0 {
		return sel
	}
	keepStart := len(sel.RetainedTail) - 1
	if keepStart < 0 {
		keepStart = 0
	}
	for keepStart > 0 && startsWithToolResult(sel.RetainedTail[keepStart]) {
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

func estimateContextTokens(systemPrompt string, tools []llm.ToolSpec, history []llm.Message) int {
	return estimateCharsAsTokens(len(systemPrompt)) +
		estimateToolTokens(tools) +
		estimateMessageTokens(history)
}
