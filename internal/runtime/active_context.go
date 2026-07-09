package runtime

import (
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

type ActiveContextSnapshot struct {
	Messages        []llm.Message `json:"messages"`
	EstimatedTokens int           `json:"estimated_tokens"`
}

func assembleActiveContext(history []llm.Message, incoming []llm.Message) ActiveContextSnapshot {
	history = providerVisibleMessages(history)
	incoming = providerVisibleMessages(incoming)
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

func providerVisibleMessages(msgs []llm.Message) []llm.Message {
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

func (e *Engine) ActiveContext(incoming ...llm.Message) ActiveContextSnapshot {
	if e == nil {
		return ActiveContextSnapshot{}
	}
	session := e.Session
	if session == nil {
		return ActiveContextSnapshot{}
	}
	_, history := session.Snapshot(time.Now().UTC())
	snap := assembleActiveContext(history, incoming)
	var contextMessages []llm.Message
	if text, ok := e.goalStateContextSnapshot(); ok {
		contextMessages = append(contextMessages, goalStateContextMessage(text))
	}
	if text, ok := e.workingStateContextSnapshot(); ok {
		contextMessages = append(contextMessages, workingStateContextMessage(text))
	}
	snap = appendRuntimeContextMessages(snap, contextMessages...)
	snap.EstimatedTokens = e.applyTokenEstimateCalibration(estimateMessageTokens(snap.Messages))
	return snap
}

func (e *Engine) activeContextLocked(incoming ...llm.Message) ActiveContextSnapshot {
	if e == nil || e.Session == nil {
		return ActiveContextSnapshot{}
	}
	snap := assembleActiveContext(e.Session.History, incoming)
	var contextMessages []llm.Message
	if text, ok := e.goalStateContextLocked(); ok {
		contextMessages = append(contextMessages, goalStateContextMessage(text))
	}
	if text, ok := e.workingStateContextLocked(); ok {
		contextMessages = append(contextMessages, workingStateContextMessage(text))
	}
	snap = appendRuntimeContextMessages(snap, contextMessages...)
	snap.EstimatedTokens = e.applyTokenEstimateCalibration(estimateMessageTokens(snap.Messages))
	return snap
}

func goalStateContextMessage(text string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, text)
	msg.ID = "runtime-goal-contract"
	msg.Kind = llm.MessageKindRuntimeContext
	return msg
}

func appendRuntimeContextMessages(snap ActiveContextSnapshot, messages ...llm.Message) ActiveContextSnapshot {
	if len(messages) == 0 {
		return snap
	}
	out := make([]llm.Message, 0, len(snap.Messages)+len(messages))
	out = append(out, snap.Messages...)
	out = append(out, messages...)
	snap.Messages = out
	snap.EstimatedTokens = estimateMessageTokens(out)
	return snap
}
