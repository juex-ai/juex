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
)

func RegisterGoalTools(reg *tools.Registry, engine *Engine) error {
	if reg == nil || engine == nil {
		return nil
	}
	if err := reg.Register(tools.Tool{
		Name:        GoalToolGet,
		Description: "Read the current session goal. Use this before deciding whether to create, update, complete, or fail a goal.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return engine.handleGetGoal()
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(tools.Tool{
		Name:        GoalToolCreate,
		Description: "Create or replace the current session goal contract. Put all completion criteria, required artifacts, constraints, and verification requirements in acceptance. The goal starts with status in_progress and belongs only to this session.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":   map[string]any{"type": "string", "description": "Concrete goal the model is trying to complete"},
				"acceptance":    map[string]any{"type": "string", "description": "Completion criteria, required artifacts, constraints, and verification requirements"},
				"status_reason": map[string]any{"type": "string", "description": "Current evidence-backed reason for the goal status"},
			},
			"required": []string{"description"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return engine.handleCreateGoal(in)
		},
	}); err != nil {
		return err
	}
	return reg.Register(tools.Tool{
		Name:        GoalToolUpdate,
		Description: "Update the current session goal contract or evidence-backed status. Set status to success only after acceptance is satisfied. When marking failure, provide status_reason to explain why.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":   map[string]any{"type": "string"},
				"acceptance":    map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string", "enum": []string{string(GoalStatusInProgress), string(GoalStatusSuccess), string(GoalStatusFailure)}},
				"status_reason": map[string]any{"type": "string", "description": "Evidence-backed reason for the current status"},
			},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return engine.handleUpdateGoal(in)
		},
	})
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
