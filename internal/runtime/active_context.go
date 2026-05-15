package runtime

import "github.com/juex-ai/juex/internal/llm"

type ActiveContextSnapshot struct {
	Messages        []llm.Message `json:"messages"`
	EstimatedTokens int           `json:"estimated_tokens"`
}

func assembleActiveContext(history []llm.Message, incoming []llm.Message) ActiveContextSnapshot {
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
		return ActiveContextSnapshot{Messages: out, EstimatedTokens: estimateMessageTokens(out)}
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
	return ActiveContextSnapshot{Messages: out, EstimatedTokens: estimateMessageTokens(out)}
}

func indexMessageID(history []llm.Message, id string) int {
	for i, msg := range history {
		if msg.ID == id {
			return i
		}
	}
	return -1
}

func ActiveContextFromHistory(history []llm.Message, incoming ...llm.Message) ActiveContextSnapshot {
	return assembleActiveContext(history, incoming)
}

func (e *Engine) ActiveContext(incoming ...llm.Message) ActiveContextSnapshot {
	if e == nil || e.Session == nil {
		return ActiveContextSnapshot{}
	}
	return assembleActiveContext(e.Session.History, incoming)
}
