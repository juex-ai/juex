package contextcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	ModuleID    runtimemodule.ID = "context-control"
	ToolNew     string           = "context_new"
	ToolCompact string           = "context_compact"
)

type Module struct {
	controller runtimemodule.ContextController
}

func New(controller runtimemodule.ContextController) *Module {
	return &Module{controller: controller}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	definitions := contextToolDefinitions()
	if m == nil || m.controller == nil {
		unavailable := func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("context control is unavailable")
		}
		return []tools.Tool{definitions[0].Bind(unavailable), definitions[1].Bind(unavailable)}, nil
	}
	return []tools.Tool{
		definitions[0].Bind(func(context.Context, map[string]any) (string, error) {
			return m.request(runtimemodule.ContextTransitionRequest{Kind: runtimemodule.ContextTransitionNew})
		}),
		definitions[1].Bind(func(_ context.Context, input map[string]any) (string, error) {
			instructions, _ := input["instructions"].(string)
			return m.request(runtimemodule.ContextTransitionRequest{Kind: runtimemodule.ContextTransitionCompact, Instructions: strings.TrimSpace(instructions)})
		}),
	}, nil
}

func (m *Module) request(request runtimemodule.ContextTransitionRequest) (string, error) {
	if err := m.controller.RequestContextTransition(request); err != nil {
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

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || m.controller == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	state := m.controller.ContextWindowState()
	if state.ThreadID == "" {
		return nil, nil
	}
	percentage := float64(state.CurrentTokens) * 100 / float64(state.WindowTokens)
	text := fmt.Sprintf("Context window: approximately %d / %d tokens (%.1f%%) in Thread %s, Generation %s. Use context_compact when this task must continue with less context; use context_new only after the task and durable notes are complete.", state.CurrentTokens, state.WindowTokens, percentage, state.ThreadID, state.GenerationID)
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
			Name:            ToolNew,
			Group:           tools.ToolGroupThreadState,
			ExecutionPolicy: tools.ToolExecutionSerial,
			Description:     "End the current task context and start an empty Context Generation. Goal and Notes are cleared; Thread working files and journal are retained. ",
			Schema:          map[string]any{"type": "object", "properties": map[string]any{}},
			TimeoutPolicy:   tools.ToolTimeoutDisabled,
		},
		{
			Name:            ToolCompact,
			Group:           tools.ToolGroupThreadState,
			ExecutionPolicy: tools.ToolExecutionSerial,
			Description:     "Summarize the current task context into a new Context Generation while retaining Goal, Notes, and Thread working files. ",
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
