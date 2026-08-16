package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	GoalToolGet    = "get_goal"
	GoalToolCreate = "create_goal"
	GoalToolUpdate = "update_goal"
	goalGuide      = `Guide available via skill_load("juex-session-state").`
)

const GoalModuleID runtimemodule.ID = "goal"

type GoalModule struct {
	engine *Engine
}

func NewGoalModule(engine *Engine) *GoalModule { return &GoalModule{engine: engine} }

func (*GoalModule) ID() runtimemodule.ID { return GoalModuleID }

func (m *GoalModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return GoalTools(m.engine), nil
}

func (m *GoalModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || m.engine == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	runtime := m.engine.SessionRuntimeSnapshot()
	text, ok := goalStateContextFromStore(runtime.GoalState)
	if !ok {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:        "session_goal",
		Source:     "runtime",
		Text:       text,
		Projection: runtimemodule.ContextProjectionRuntimeMessage,
		MessageID:  "runtime-goal-contract",
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func GoalToolDefinitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        GoalToolGet,
			Group:       tools.ToolGroupSessionState,
			Description: "Read the current session goal before changing it. " + goalGuide,
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        GoalToolCreate,
			Group:       tools.ToolGroupSessionState,
			Description: "Create or replace this session's in-progress goal contract. " + goalGuide,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description":   map[string]any{"type": "string"},
					"acceptance":    map[string]any{"type": "string"},
					"status_reason": map[string]any{"type": "string"},
				},
				"required": []string{"description"},
			},
		},
		{
			Name:        GoalToolUpdate,
			Group:       tools.ToolGroupSessionState,
			Description: "Update goal fields or status (in_progress, wait_for_user, success, or failure). Use wait_for_user only when progress requires new external input; success requires acceptance. " + goalGuide,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description":   map[string]any{"type": "string"},
					"acceptance":    map[string]any{"type": "string"},
					"status":        map[string]any{"type": "string"},
					"status_reason": map[string]any{"type": "string"},
				},
			},
		},
	}
}

func GoalTools(engine *Engine) []tools.Tool {
	definitions := GoalToolDefinitions()
	unavailable := func(context.Context, map[string]any) (string, error) {
		return "", fmt.Errorf("goal state is not configured")
	}
	if engine == nil {
		return []tools.Tool{definitions[0].Bind(unavailable), definitions[1].Bind(unavailable), definitions[2].Bind(unavailable)}
	}
	return []tools.Tool{
		definitions[0].Bind(func(context.Context, map[string]any) (string, error) { return engine.handleGetGoal() }),
		definitions[1].Bind(func(_ context.Context, in map[string]any) (string, error) { return engine.handleCreateGoal(in) }),
		definitions[2].Bind(func(_ context.Context, in map[string]any) (string, error) { return engine.handleUpdateGoal(in) }),
	}
}

func (e *Engine) handleGetGoal() (string, error) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return "", fmt.Errorf("goal state is not configured")
	}
	snapshot, err := store.StatusSnapshot()
	if err != nil {
		return "", err
	}
	if snapshot == nil {
		return marshalGoalToolResponse(map[string]any{"present": false})
	}
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": snapshot})
}

func (e *Engine) handleCreateGoal(in map[string]any) (string, error) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return "", fmt.Errorf("goal state is not configured")
	}
	description := goalToolString(in, "description")
	state, err := store.CreateWithContract(GoalStateCreate{
		Description:  description,
		Acceptance:   goalToolString(in, "acceptance"),
		StatusReason: goalToolString(in, "status_reason"),
	})
	if err != nil {
		return "", err
	}
	e.emitGoalUpdated(e.activeTurnID)
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": state.StatusSnapshot()})
}

func (e *Engine) handleUpdateGoal(in map[string]any) (string, error) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return "", fmt.Errorf("goal state is not configured")
	}
	var update GoalStateUpdate
	changed := false
	if _, ok := in["description"]; ok {
		value := goalToolString(in, "description")
		update.Description = &value
		changed = true
	}
	if _, ok := in["acceptance"]; ok {
		value := goalToolString(in, "acceptance")
		update.Acceptance = &value
		changed = true
	}
	if raw := goalToolString(in, "status"); raw != "" {
		update.Status = GoalStatus(raw)
		changed = true
	}
	if _, ok := in["status_reason"]; ok {
		value := goalToolString(in, "status_reason")
		update.StatusReason = &value
		changed = true
	}
	if !changed {
		return "", fmt.Errorf("update_goal requires at least one goal contract or status field")
	}
	state, err := store.Update(update)
	if err != nil {
		return "", err
	}
	e.emitGoalUpdated(e.activeTurnID)
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": state.StatusSnapshot()})
}

func marshalGoalToolResponse(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func goalToolString(in map[string]any, key string) string {
	if in == nil {
		return ""
	}
	value, _ := in[key].(string)
	return strings.TrimSpace(value)
}
