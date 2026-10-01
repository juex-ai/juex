package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func validModelPlan(plan managedruntime.TurnConfig) bool {
	if len(plan.Models) < 1 || len(plan.Models) > 5 || hookpolicy.Validate(plan.Hooks) != nil {
		return false
	}
	seen := map[string]bool{}
	for _, model := range plan.Models {
		if model.ModelID == "" || model.Model == "" || model.Provider == "" || model.ContextWindow < 1024 || model.MaxOutput <= 0 || model.MaxOutput >= model.ContextWindow || seen[model.ModelID] {
			return false
		}
		seen[model.ModelID] = true
	}
	return true
}

func activeModelPlan(ctx context.Context, tx pgx.Tx, turn string, epoch int64) (managedruntime.TurnConfig, int, error) {
	var encoded []byte
	var plan managedruntime.TurnConfig
	var index int
	err := tx.QueryRow(ctx, `SELECT config,model_index FROM runtime.turns WHERE id=$1 AND activation_epoch=$2 AND state='running'`, turn, epoch).Scan(&encoded, &index)
	if err != nil {
		return plan, index, classify(err)
	}
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return plan, index, err
	}
	if !validModelPlan(plan) || index < 0 || index >= len(plan.Models) {
		return plan, index, managedruntime.ErrInvalid
	}
	return plan, index, nil
}

// AdvanceModel records skipped candidates before continuing. A replacement
// Activation therefore never resets a failed or revoked candidate to primary.
func (s *Store) AdvanceModel(ctx context.Context, lease managedruntime.Lease, turn string, index int, reason string) error {
	if reason != "model_unavailable" && reason != "context_limit" {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return err
	}
	var thread string
	if err := tx.QueryRow(ctx, `SELECT t.thread_id FROM runtime.turns t JOIN runtime.threads th ON th.id=t.thread_id WHERE t.id=$1 AND th.agent_id=$2`, turn, lease.AgentID).Scan(&thread); err != nil {
		return classify(err)
	}
	if _, err := readThread(ctx, tx, lease.AgentID, thread); err != nil {
		return err
	}
	plan, current, err := activeModelPlan(ctx, tx, turn, lease.Epoch)
	if err != nil {
		return err
	}
	if index != current {
		return managedruntime.ErrConflict
	}
	var started bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.attempts WHERE turn_id=$1 AND state='started')`, turn).Scan(&started); err != nil {
		return err
	}
	if started {
		return managedruntime.ErrConflict
	}
	if index+1 >= len(plan.Models) {
		return managedruntime.ErrModelUnavailable
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET model_index=model_index+1 WHERE id=$1`, turn); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread, "model.fallback", map[string]any{"turn_id": turn, "from_model_id": plan.Models[index].ModelID, "to_model_id": plan.Models[index+1].ModelID, "to_model": plan.Models[index+1].Provider + ":" + plan.Models[index+1].Model, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func modelOrigins(ctx context.Context, tx pgx.Tx, history []llm.Message) (map[string]managedruntime.ModelConfig, error) {
	var ids []string
	for _, message := range history {
		if message.Role == llm.RoleAssistant {
			if _, err := uuid.Parse(message.ID); err == nil {
				ids = append(ids, message.ID)
			}
		}
	}
	result := map[string]managedruntime.ModelConfig{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,request->'model' FROM runtime.attempts WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var encoded []byte
		var model managedruntime.ModelConfig
		if err := rows.Scan(&id, &encoded); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &model); err != nil {
			return nil, err
		}
		result[id] = model
	}
	return result, rows.Err()
}
