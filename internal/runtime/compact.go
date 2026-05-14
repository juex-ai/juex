package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	DefaultContextWindowTokens = 256000
	compactThresholdPercent    = 80
)

func (e *Engine) maybeCompact(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, incoming llm.Message) error {
	contextWindow := e.ContextWindow
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	threshold := contextWindow * compactThresholdPercent / 100
	if threshold <= 0 {
		return nil
	}

	projected := append(providerHistory(e.Session.History), incoming)
	estimated := estimateContextTokens(systemPrompt, tools, projected)
	if estimated < threshold {
		return nil
	}

	historyToCompact := providerHistory(e.Session.History)
	if len(historyToCompact) == 0 {
		return nil
	}
	e.emit(events.Event{Type: "context.compact.started", TurnID: turnID, Payload: map[string]any{
		"estimated_tokens": estimated,
		"context_window":   contextWindow,
		"threshold":        threshold,
	}})

	resp, err := e.Provider.Complete(ctx, compactSystemPrompt(systemPrompt), historyToCompact, nil)
	if err != nil {
		return fmt.Errorf("compact context: %w", err)
	}
	summary := strings.TrimSpace(responseText(resp.Message))
	if summary == "" {
		summary = "(summary unavailable)"
	}

	msg := llm.TextMessage(llm.RoleUser, compactMessageText(summary))
	msg.Kind = llm.MessageKindCompact
	if err := e.Session.Append(msg); err != nil {
		return fmt.Errorf("session append compact: %w", err)
	}
	e.emit(events.Event{Type: "context.compact.completed", TurnID: turnID, Payload: map[string]any{
		"estimated_tokens": estimated,
		"context_window":   contextWindow,
		"threshold":        threshold,
	}})
	return nil
}

func compactSystemPrompt(base string) string {
	return strings.TrimSpace(base + "\n\n" + `You are preparing a compact summary for continuing this conversation.

Summarize the conversation so a future assistant can continue with the same facts, user preferences, decisions, files changed, commands run, errors seen, and remaining tasks.

Do not answer the latest user request. Do not call tools. Return only the compact summary.`)
}

func compactMessageText(summary string) string {
	return "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\n" + summary
}

func providerHistory(history []llm.Message) []llm.Message {
	start := 0
	for i := range history {
		if history[i].Kind == llm.MessageKindCompact {
			start = i
		}
	}
	out := make([]llm.Message, len(history)-start)
	copy(out, history[start:])
	return out
}

func estimateContextTokens(systemPrompt string, tools []llm.ToolSpec, history []llm.Message) int {
	chars := len(systemPrompt)
	if len(tools) > 0 {
		if data, err := json.Marshal(tools); err == nil {
			chars += len(data)
		}
	}
	for _, m := range history {
		chars += len(m.Role) + len(m.Kind) + 8
		for _, b := range m.Blocks {
			chars += len(b.Type) + len(b.Text) + len(b.Content) + len(b.ToolUseID) + len(b.ToolName) + 8
			if len(b.Input) > 0 {
				if data, err := json.Marshal(b.Input); err == nil {
					chars += len(data)
				}
			}
		}
	}
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}
