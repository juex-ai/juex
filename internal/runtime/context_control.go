package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	ContextControlModuleID runtimemodule.ID = "context-control"
	ContextToolNew         string           = "context_new"
	ContextToolCompact     string           = "context_compact"
)

const contextControlGuide = `Guide available via skill_load("juex-thread-state").`

type contextTransitionKind string

const (
	contextTransitionNew     contextTransitionKind = "new"
	contextTransitionCompact contextTransitionKind = "compact"
)

type contextTransitionRequest struct {
	Kind         contextTransitionKind
	Instructions string
}

type contextPromptInputs struct {
	SystemPrompt string
	Tools        []llm.ToolSpec
}

type ContextControlModule struct {
	engine *Engine
}

func NewContextControlModule(engine *Engine) *ContextControlModule {
	return &ContextControlModule{engine: engine}
}

func (*ContextControlModule) ID() runtimemodule.ID { return ContextControlModuleID }

func (m *ContextControlModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	definitions := contextToolDefinitions()
	if m == nil || m.engine == nil {
		unavailable := func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("context control is unavailable")
		}
		return []tools.Tool{definitions[0].Bind(unavailable), definitions[1].Bind(unavailable)}, nil
	}
	return []tools.Tool{
		definitions[0].Bind(func(context.Context, map[string]any) (string, error) {
			return m.request(contextTransitionRequest{Kind: contextTransitionNew})
		}),
		definitions[1].Bind(func(_ context.Context, input map[string]any) (string, error) {
			instructions, _ := input["instructions"].(string)
			return m.request(contextTransitionRequest{Kind: contextTransitionCompact, Instructions: strings.TrimSpace(instructions)})
		}),
	}, nil
}

func (m *ContextControlModule) request(request contextTransitionRequest) (string, error) {
	if err := m.engine.requestContextTransition(request); err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"accepted": true,
		"action":   request.Kind,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (m *ContextControlModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || m.engine == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	text := m.engine.contextWindowRecitation()
	if text == "" {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:        "context_window",
		Label:      "Context Window",
		Source:     "runtime",
		Text:       text,
		Projection: runtimemodule.ContextProjectionRuntimeMessage,
		MessageID:  "runtime-context-window",
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func contextToolDefinitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:          ContextToolNew,
			Group:         tools.ToolGroupThreadState,
			Description:   "End the current task context and start an empty Context Generation. Goal and Notes are cleared; the Thread scratchpad and journal are retained. " + contextControlGuide,
			Schema:        map[string]any{"type": "object", "properties": map[string]any{}},
			TimeoutPolicy: tools.ToolTimeoutDisabled,
		},
		{
			Name:        ContextToolCompact,
			Group:       tools.ToolGroupThreadState,
			Description: "Summarize the current task context into a new Context Generation while retaining Goal, Notes, and the Thread scratchpad. " + contextControlGuide,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"instructions": map[string]any{"type": "string"},
				},
			},
			TimeoutPolicy: tools.ToolTimeoutDisabled,
		},
	}
}

func (e *Engine) requestContextTransition(request contextTransitionRequest) error {
	if e == nil {
		return fmt.Errorf("runtime: engine is required")
	}
	if request.Kind != contextTransitionNew && request.Kind != contextTransitionCompact {
		return fmt.Errorf("runtime: unsupported context transition %q", request.Kind)
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

func (e *Engine) takeContextTransition() *contextTransitionRequest {
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

func (e *Engine) applyRequestedContextTransitionLocked(ctx context.Context, turnID string, prepared preparedTurnContext) (contextTransitionKind, error) {
	request := e.takeContextTransition()
	if request == nil {
		return "", nil
	}
	switch request.Kind {
	case contextTransitionCompact:
		_, err := e.compactLocked(ctx, turnID, prepared.systemPrompt, prepared.tools, "agent", false, request.Instructions, e.activeOperationGenerationSnapshot())
		return request.Kind, err
	case contextTransitionNew:
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

func (e *Engine) contextWindowRecitation() string {
	if e == nil {
		return ""
	}
	runtime := e.ThreadRuntimeSnapshot()
	if runtime.Thread == nil {
		return ""
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
	percentage := float64(current) * 100 / float64(window)
	return fmt.Sprintf(
		"Context window: approximately %d / %d tokens (%.1f%%) in Thread %s, Generation %s. Use context_compact when this task must continue with less context; use context_new only after the task and durable notes are complete.",
		current, window, percentage, projection.ThreadID, projection.CurrentGeneration.ID,
	)
}

var _ runtimemodule.ToolProvider = (*ContextControlModule)(nil)
var _ runtimemodule.ContextProvider = (*ContextControlModule)(nil)
