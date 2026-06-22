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
		Description: "Create or replace the current session goal. The goal starts with status in_progress and belongs only to this session.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":         map[string]any{"type": "string", "description": "Concrete goal the model is trying to complete"},
				"verification_method": map[string]any{"type": "string", "description": "How completion should be verified"},
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
		Description: "Update the current session goal description, verification method, or status. Set status to success when verified complete, or failure when it cannot be completed.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":         map[string]any{"type": "string"},
				"verification_method": map[string]any{"type": "string"},
				"status":              map[string]any{"type": "string", "enum": []string{string(GoalStatusInProgress), string(GoalStatusSuccess), string(GoalStatusFailure)}},
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
	verificationMethod := goalToolString(in, "verification_method")
	state, err := store.Create(description, verificationMethod)
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
	if _, ok := in["verification_method"]; ok {
		value := goalToolString(in, "verification_method")
		update.VerificationMethod = &value
		changed = true
	}
	if raw := goalToolString(in, "status"); raw != "" {
		update.Status = GoalStatus(raw)
		changed = true
	}
	if !changed {
		return "", fmt.Errorf("update_goal requires description, verification_method, or status")
	}
	state, err := store.Update(update)
	if err != nil {
		return "", err
	}
	e.emitGoalUpdated(e.activeTurnID)
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": state.StatusSnapshot()})
}

func marshalGoalToolResponse(value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
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
