package contextbudget

import "github.com/juex-ai/juex/internal/llm"

type ActiveContextSnapshot struct {
	Messages        []llm.Message `json:"messages"`
	EstimatedTokens int           `json:"estimated_tokens"`
}

func AssembleActiveContext(history []llm.Message, incoming []llm.Message) ActiveContextSnapshot {
	history = ProviderVisibleMessages(history)
	incoming = ProviderVisibleMessages(incoming)
	latestCompact := -1
	for i := range history {
		if history[i].Kind == llm.MessageKindCompact {
			latestCompact = i
		}
	}
	out := make([]llm.Message, 0, len(history)+len(incoming))
	if latestCompact < 0 {
		out = append(out, history...)
		out = append(out, incoming...)
		return ActiveContextSnapshot{Messages: out, EstimatedTokens: EstimateMessageTokens(out)}
	}

	compact := history[latestCompact]
	out = append(out, compact)
	if compact.Compaction != nil && compact.Compaction.TailStartMessageID != "" {
		if tailStart := indexMessageID(history[:latestCompact], compact.Compaction.TailStartMessageID); tailStart >= 0 {
			out = append(out, history[tailStart:latestCompact]...)
		}
	}
	out = append(out, history[latestCompact+1:]...)
	out = append(out, incoming...)
	return ActiveContextSnapshot{Messages: out, EstimatedTokens: EstimateMessageTokens(out)}
}

func ActiveContextFromHistory(history []llm.Message, incoming ...llm.Message) ActiveContextSnapshot {
	return AssembleActiveContext(history, incoming)
}

func ProviderVisibleMessages(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	var filtered []llm.Message
	for i, msg := range msgs {
		if msg.Kind != llm.MessageKindHookEvent {
			if filtered != nil {
				filtered = append(filtered, msg)
			}
			continue
		}
		if filtered == nil {
			filtered = make([]llm.Message, 0, len(msgs)-1)
			filtered = append(filtered, msgs[:i]...)
		}
	}
	if filtered == nil {
		return msgs
	}
	return filtered
}

func indexMessageID(history []llm.Message, id string) int {
	for i, msg := range history {
		if msg.ID == id {
			return i
		}
	}
	return -1
}
