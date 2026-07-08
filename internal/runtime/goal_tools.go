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
		Description: "Create or replace the current session goal contract. Include concrete acceptance criteria, required artifacts, and validation requirements when they are known. The goal starts with status in_progress and belongs only to this session.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":             map[string]any{"type": "string", "description": "Concrete goal the model is trying to complete"},
				"acceptance_criteria":     goalToolStringArraySchema("Observable conditions that must be true before the goal can be marked success"),
				"required_artifacts":      goalToolStringArraySchema("Files, outputs, PRs, docs, or other artifacts that must exist for completion"),
				"artifact_requirements":   goalToolStringArraySchema("Constraints the required artifacts must satisfy"),
				"validation_requirements": goalToolStringArraySchema("Tests, commands, checks, or evidence required before success"),
				"verification_method":     map[string]any{"type": "string", "description": "Short summary of how completion should be verified"},
				"status_reason":           map[string]any{"type": "string", "description": "Current evidence-backed reason for the goal status"},
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
		Description: "Update the current session goal contract or evidence-backed status. Set status to success only after the acceptance criteria and validation requirements are satisfied, or failure when it cannot be completed.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description":             map[string]any{"type": "string"},
				"acceptance_criteria":     goalToolStringArraySchema("Replace the observable completion criteria"),
				"required_artifacts":      goalToolStringArraySchema("Replace the required completion artifacts"),
				"artifact_requirements":   goalToolStringArraySchema("Replace constraints for required artifacts"),
				"validation_requirements": goalToolStringArraySchema("Replace validation requirements"),
				"verification_method":     map[string]any{"type": "string"},
				"status":                  map[string]any{"type": "string", "enum": []string{string(GoalStatusInProgress), string(GoalStatusSuccess), string(GoalStatusFailure)}},
				"status_reason":           map[string]any{"type": "string", "description": "Evidence-backed reason for the current status"},
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
		Description:            description,
		AcceptanceCriteria:     goalToolStringList(in, "acceptance_criteria"),
		RequiredArtifacts:      goalToolStringList(in, "required_artifacts"),
		ArtifactRequirements:   goalToolStringList(in, "artifact_requirements"),
		ValidationRequirements: goalToolStringList(in, "validation_requirements"),
		VerificationMethod:     goalToolString(in, "verification_method"),
		StatusReason:           goalToolString(in, "status_reason"),
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
	if value, ok := goalToolStringListIfPresent(in, "acceptance_criteria"); ok {
		update.AcceptanceCriteria = &value
		changed = true
	}
	if value, ok := goalToolStringListIfPresent(in, "required_artifacts"); ok {
		update.RequiredArtifacts = &value
		changed = true
	}
	if value, ok := goalToolStringListIfPresent(in, "artifact_requirements"); ok {
		update.ArtifactRequirements = &value
		changed = true
	}
	if value, ok := goalToolStringListIfPresent(in, "validation_requirements"); ok {
		update.ValidationRequirements = &value
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

func goalToolStringArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       map[string]any{"type": "string"},
	}
}

func goalToolStringList(in map[string]any, key string) []string {
	values, _ := goalToolStringListIfPresent(in, key)
	return values
}

func goalToolStringListIfPresent(in map[string]any, key string) ([]string, bool) {
	if in == nil {
		return nil, false
	}
	raw, ok := in[key]
	if !ok {
		return nil, false
	}
	return goalToolStringListValue(raw), true
}

func goalToolStringListValue(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return sanitizeGoalTextList(values)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return sanitizeGoalTextList(out)
	case string:
		return sanitizeGoalTextList([]string{values})
	default:
		return nil
	}
}
