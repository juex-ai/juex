package runtime

import (
	"context"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type contextPromptInputs struct {
	SystemPrompt string
	Tools        []llm.ToolSpec
}

func (e *Engine) RequestContextTransition(request runtimemodule.ContextTransitionRequest) error {
	if e == nil {
		return fmt.Errorf("runtime: engine is required")
	}
	if request.Kind != runtimemodule.ContextTransitionNew && request.Kind != runtimemodule.ContextTransitionCompact {
		return fmt.Errorf("runtime: unsupported context transition %q", request.Kind)
	}
	if request.Kind == runtimemodule.ContextTransitionNew {
		if err := e.checkInputScopeRenewal(); err != nil {
			return err
		}
	}
	e.contextControlMu.Lock()
	defer e.contextControlMu.Unlock()
	if e.pendingContextTransition != nil {
		return fmt.Errorf("runtime: context transition %q is already requested", e.pendingContextTransition.Kind)
	}
	copy := request
	e.pendingContextTransition = &copy
	return nil
}

func (e *Engine) takeContextTransition() *runtimemodule.ContextTransitionRequest {
	e.contextControlMu.Lock()
	defer e.contextControlMu.Unlock()
	request := e.pendingContextTransition
	e.pendingContextTransition = nil
	return request
}

func (e *Engine) clearContextTransition() {
	e.contextControlMu.Lock()
	e.pendingContextTransition = nil
	e.contextControlMu.Unlock()
}

func (e *Engine) applyRequestedContextTransitionLocked(ctx context.Context, turnID string, prepared preparedTurnContext) (runtimemodule.ContextTransitionKind, error) {
	request := e.takeContextTransition()
	if request == nil {
		return "", nil
	}
	switch request.Kind {
	case runtimemodule.ContextTransitionCompact:
		_, err := e.compactLocked(ctx, turnID, prepared.systemPrompt, prepared.tools, "agent", false, request.Instructions, e.activeOperationGenerationSnapshot())
		return request.Kind, err
	case runtimemodule.ContextTransitionNew:
		return request.Kind, e.newContextLocked(ctx, true)
	default:
		return "", fmt.Errorf("runtime: unsupported context transition %q", request.Kind)
	}
}

func (e *Engine) activeOperationGenerationSnapshot() uint64 {
	e.activeOperationMu.Lock()
	defer e.activeOperationMu.Unlock()
	return e.activeOperationGeneration
}

func (e *Engine) setContextPromptInputs(systemPrompt string, toolSpecs []llm.ToolSpec) {
	e.contextPromptMu.Lock()
	e.contextPromptInputs = contextPromptInputs{SystemPrompt: systemPrompt, Tools: append([]llm.ToolSpec(nil), toolSpecs...)}
	e.contextPromptMu.Unlock()
}

func (e *Engine) ContextWindowState() runtimemodule.ContextWindowState {
	if e == nil {
		return runtimemodule.ContextWindowState{}
	}
	runtime := e.ThreadRuntimeSnapshot()
	if runtime.Thread == nil {
		return runtimemodule.ContextWindowState{}
	}
	projection, history := runtime.Thread.Snapshot()
	e.contextPromptMu.Lock()
	inputs := e.contextPromptInputs
	e.contextPromptMu.Unlock()
	current := e.estimateContextTokens(inputs.SystemPrompt, inputs.Tools, history)
	if usage := runtime.Thread.ContextUsageSnapshot(); usage != nil && usage.TotalTokens > current {
		current = usage.TotalTokens
	}
	window := e.ContextWindow
	if window <= 0 {
		window = DefaultContextWindowTokens
	}
	return runtimemodule.ContextWindowState{CurrentTokens: current, WindowTokens: window, ThreadID: projection.ThreadID, GenerationID: projection.CurrentGeneration.ID}
}

var _ runtimemodule.ContextController = (*Engine)(nil)
