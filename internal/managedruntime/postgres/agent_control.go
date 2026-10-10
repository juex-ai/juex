package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// A tool can prepare a management mutation only from an ordinary Main, with
// both frozen permission and the live tool fence. Preparation orders cancellation.
func agentControlFence(ctx context.Context, tx pgx.Tx, work managedruntime.ToolWork) error {
	if err := subscriptionAction(ctx, tx, work); err != nil {
		return err
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id JOIN runtime.threads th ON th.id=t.thread_id JOIN runtime.inputs i ON i.id=t.input_id WHERE j.id=$1 AND t.id=$2 AND th.id=$3 AND th.agent_id=$4 AND th.kind='main' AND th.application='' AND COALESCE(i.source->>'application','')='' AND COALESCE((t.config->>'agent_management')::boolean,false))`, work.ID, work.TurnID, work.ThreadID, work.Scope.AgentID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return managedruntime.ErrDenied
	}
	return nil
}

func (s *Store) CheckAgentControl(ctx context.Context, work managedruntime.ToolWork) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = agentControlFence(ctx, tx, work); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PrepareAgentControl(ctx context.Context, work managedruntime.ToolWork, action agentcontrol.Action) error {
	if !action.Valid() {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = agentControlFence(ctx, tx, work); err != nil {
		return err
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		return err
	}
	var same bool
	err = tx.QueryRow(ctx, `UPDATE runtime.tools SET agent_control=COALESCE(agent_control,$3::jsonb) WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp() RETURNING agent_control=$3::jsonb`, work.ID, work.LeaseEpoch, encoded).Scan(&same)
	if err != nil {
		return classify(err)
	}
	if !same {
		return managedruntime.ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) ControlAgentLifecycle(ctx context.Context, work managedruntime.ToolWork, target managedruntime.Scope, change managedruntime.AgentLifecycleChange) (managedruntime.AgentLifecycleReceipt, error) {
	var result managedruntime.AgentLifecycleReceipt
	if target.AgentID == work.Scope.AgentID || target.ActorID != work.Scope.ActorID || target.FleetID != work.Scope.FleetID || target.TenantID != work.Scope.TenantID || target.UserID != work.Scope.UserID || !work.Scope.AgentManagement || !work.FrozenAgentManagement {
		return result, managedruntime.ErrDenied
	}
	if change.RequestID != work.ID || change.Validate() != nil {
		return result, managedruntime.ErrInvalid
	}
	if _, err := s.EnsureAgent(ctx, target); err != nil {
		return result, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = lockAgentAdmission(ctx, tx, work.Scope, target); err != nil {
		return result, err
	}
	if err = agentControlFence(ctx, tx, work); err != nil {
		return result, err
	}
	result, err = changeAgentLifecycle(ctx, tx, target, change)
	if err != nil {
		return result, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime.tools SET agent_lifecycle=$2 WHERE id=$1`, work.ID, encoded); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
