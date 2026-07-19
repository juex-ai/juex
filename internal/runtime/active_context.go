package runtime

import (
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

type ActiveContextSnapshot = contextbudget.ActiveContextSnapshot

func assembleActiveContext(history []llm.Message, incoming []llm.Message) ActiveContextSnapshot {
	return contextbudget.AssembleActiveContext(history, incoming)
}

func ActiveContextFromHistory(history []llm.Message, incoming ...llm.Message) ActiveContextSnapshot {
	return contextbudget.ActiveContextFromHistory(history, incoming...)
}

func providerVisibleMessages(msgs []llm.Message) []llm.Message {
	return contextbudget.ProviderVisibleMessages(msgs)
}

func (e *Engine) ActiveContext(incoming ...llm.Message) ActiveContextSnapshot {
	if e == nil {
		return ActiveContextSnapshot{}
	}
	runtime := e.SessionRuntimeSnapshot()
	if runtime.Session == nil {
		return ActiveContextSnapshot{}
	}
	_, history := runtime.Session.Snapshot(time.Now().UTC())
	snap := assembleActiveContext(history, incoming)
	var contextMessages []llm.Message
	if text, ok := goalStateContextFromStore(runtime.GoalState); ok {
		contextMessages = append(contextMessages, goalStateContextMessage(text))
	}
	if text, ok := e.notesContextFromStore(runtime.Notes); ok {
		contextMessages = append(contextMessages, notesContextMessage(text))
	}
	contextMessages = append(contextMessages, e.pendingHookRuntimeContextSnapshot()...)
	snap = appendRuntimeContextMessages(snap, contextMessages...)
	snap.EstimatedTokens = e.estimateMessageTokens(snap.Messages)
	return snap
}

func (e *Engine) activeContextLocked(incoming ...llm.Message) ActiveContextSnapshot {
	return e.activeContextLockedWithHookContext(e.pendingHookRuntimeContextSnapshot(), incoming...)
}

func (e *Engine) activeContextLockedWithHookContext(hookContext []llm.Message, incoming ...llm.Message) ActiveContextSnapshot {
	runtime := e.SessionRuntimeSnapshot()
	if e == nil || runtime.Session == nil {
		return ActiveContextSnapshot{}
	}
	snap := assembleActiveContext(runtime.Session.History, incoming)
	var contextMessages []llm.Message
	if text, ok := goalStateContextFromStore(runtime.GoalState); ok {
		contextMessages = append(contextMessages, goalStateContextMessage(text))
	}
	if text, ok := e.notesContextFromStore(runtime.Notes); ok {
		contextMessages = append(contextMessages, notesContextMessage(text))
	}
	contextMessages = append(contextMessages, hookContext...)
	snap = appendRuntimeContextMessages(snap, contextMessages...)
	snap.EstimatedTokens = e.estimateMessageTokens(snap.Messages)
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
