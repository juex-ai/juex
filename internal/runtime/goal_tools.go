package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/tools"
)

const (
	GoalToolGet    = "get_goal"
	GoalToolCreate = "create_goal"
	GoalToolUpdate = "update_goal"
	goalGuide      = "MUST load the `juex-session-state` skill before first use."
)

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
			Description: "Update goal fields or evidence-backed status; success requires acceptance. " + goalGuide,
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

func RegisterGoalTools(reg *tools.Registry, engine *Engine) error {
	if reg == nil || engine == nil {
		return nil
	}
	definitions := GoalToolDefinitions()
	if err := reg.Register(definitions[0].Bind(func(ctx context.Context, in map[string]any) (string, error) {
		return engine.handleGetGoal()
	})); err != nil {
		return err
	}
	if err := reg.Register(definitions[1].Bind(func(ctx context.Context, in map[string]any) (string, error) {
		return engine.handleCreateGoal(in)
	})); err != nil {
		return err
	}
	return reg.Register(definitions[2].Bind(func(ctx context.Context, in map[string]any) (string, error) {
		return engine.handleUpdateGoal(in)
	}))
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
