package runtime

import (
	"context"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
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
	snapshot, _ := e.ActiveContextWithError(context.Background(), incoming...)
	return snapshot
}

func (e *Engine) ActiveContextWithError(ctx context.Context, incoming ...llm.Message) (ActiveContextSnapshot, error) {
	if e == nil {
		return ActiveContextSnapshot{}, nil
	}
	runtime := e.ThreadRuntimeSnapshot()
	if runtime.Thread == nil {
		return ActiveContextSnapshot{}, nil
	}
	_, history := runtime.Thread.Snapshot()
	snap := assembleActiveContext(history, incoming)
	contextMessages, err := e.moduleRuntimeContextMessages(ctx, runtime)
	if err != nil {
		return ActiveContextSnapshot{}, err
	}
	contextMessages = append(contextMessages, e.pendingPolicyRuntimeContextSnapshot()...)
	snap = appendRuntimeContextMessages(snap, contextMessages...)
	snap.EstimatedTokens = e.estimateMessageTokens(snap.Messages)
	return snap, nil
}

func (e *Engine) activeContextLocked(incoming ...llm.Message) ActiveContextSnapshot {
	return e.activeContextLockedWithPolicyContext(e.pendingPolicyRuntimeContextSnapshot(), incoming...)
}

func (e *Engine) activeContextLockedWithPolicyContext(policyContext []llm.Message, incoming ...llm.Message) ActiveContextSnapshot {
	snapshot, _ := e.activeContextLockedWithPolicyContextError(context.Background(), policyContext, incoming...)
	return snapshot
}

func (e *Engine) activeContextLockedWithPolicyContextError(ctx context.Context, policyContext []llm.Message, incoming ...llm.Message) (ActiveContextSnapshot, error) {
	if e == nil {
		return ActiveContextSnapshot{}, nil
	}
	runtime := e.ThreadRuntimeSnapshot()
	if runtime.Thread == nil {
		return ActiveContextSnapshot{}, nil
	}
	_, history := runtime.Thread.Snapshot()
	snap := assembleActiveContext(history, incoming)
	contextMessages, err := e.moduleRuntimeContextMessages(ctx, runtime)
	if err != nil {
		return ActiveContextSnapshot{}, err
	}
	contextMessages = append(contextMessages, policyContext...)
	snap = appendRuntimeContextMessages(snap, contextMessages...)
	snap.EstimatedTokens = e.estimateMessageTokens(snap.Messages)
	return snap, nil
}

func (e *Engine) moduleRuntimeContextMessages(ctx context.Context, runtime ThreadRuntimeSnapshot) ([]llm.Message, error) {
	if runtime.Thread == nil {
		return nil, nil
	}
	threadContext := runtimemodule.ThreadContext{
		ID:            runtime.Thread.ID,
		Dir:           runtime.Thread.Dir,
		ScratchpadDir: runtime.ScratchpadDir,
	}
	sections, err := runtimemodule.CollectContext(ctx, runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Runtime: runtimemodule.RuntimeContext{
			WorkDir:  e.WorkDir,
			MediaDir: e.MediaDir,
		},
		Thread: &threadContext,
	}, e.RuntimeModules, runtime.Modules)
	if err != nil {
		return nil, err
	}
	sections = runtimemodule.SectionsForProjection(sections, runtimemodule.ContextProjectionRuntimeMessage)
	messages := make([]llm.Message, 0, len(sections))
	for _, section := range sections {
		message := llm.TextMessage(llm.RoleUser, section.Text)
		message.ID = section.MessageID
		message.Kind = llm.MessageKindRuntimeContext
		messages = append(messages, message)
	}
	return messages, nil
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
