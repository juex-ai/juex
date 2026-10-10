package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// Admission locks precede graph, job and Thread locks. NO KEY UPDATE keeps the
// immutable identity available to FK checks in independent completion writers.
func lockAgentAdmission(ctx context.Context, tx pgx.Tx, scopes ...managedruntime.Scope) error {
	scopes = slices.Clone(scopes)
	slices.SortFunc(scopes, func(a, b managedruntime.Scope) int {
		if a.AgentID < b.AgentID {
			return -1
		}
		if a.AgentID > b.AgentID {
			return 1
		}
		return 0
	})
	for _, scope := range scopes {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM runtime.agents WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4 AND NOT purging FOR NO KEY UPDATE`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&id); err != nil {
			return classify(err)
		}
	}
	return nil
}

// The caller already holds the Agent admission lock. Receipt reads and cleanup
// may proceed while paused; only creation of new responsibility uses this gate.
func requireAgentRunning(ctx context.Context, tx pgx.Tx, agent string) error {
	var running bool
	if err := tx.QueryRow(ctx, `SELECT run_mode='running' FROM runtime.agents WHERE id=$1`, agent).Scan(&running); err != nil {
		return classify(err)
	}
	if !running {
		return managedruntime.ErrPaused
	}
	return nil
}

func agentRunState(ctx context.Context, tx pgx.Tx, agent string) (managedruntime.AgentRunState, error) {
	value := managedruntime.AgentRunState{Initialized: true, AgentID: agent, Busy: []string{}}
	if err := tx.QueryRow(ctx, `SELECT run_mode,lifecycle_version,epoch FROM runtime.agents WHERE id=$1`, agent).Scan(&value.Mode, &value.Version, &value.ActivationEpoch); err != nil {
		return value, classify(err)
	}
	rows, err := tx.Query(ctx, `SELECT reason FROM (
 SELECT 'inputs' reason WHERE EXISTS(SELECT 1 FROM runtime.inputs i JOIN runtime.threads t ON t.id=i.thread_id WHERE t.agent_id=$1 AND i.state IN ('queued','active','held'))
 UNION ALL SELECT 'turns' WHERE EXISTS(SELECT 1 FROM runtime.turns r JOIN runtime.threads t ON t.id=r.thread_id WHERE t.agent_id=$1 AND r.state IN ('running','waiting'))
 UNION ALL SELECT 'provider_attempts' WHERE EXISTS(SELECT 1 FROM runtime.attempts a JOIN runtime.turns r ON r.id=a.turn_id JOIN runtime.threads t ON t.id=r.thread_id WHERE t.agent_id=$1 AND a.state='started')
 UNION ALL SELECT 'tools' WHERE EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns r ON r.id=j.turn_id JOIN runtime.threads t ON t.id=r.thread_id WHERE t.agent_id=$1 AND (j.state IN ('pending','waiting','unknown') OR j.operation_live))
 UNION ALL SELECT 'hooks' WHERE EXISTS(SELECT 1 FROM runtime.hooks j JOIN runtime.threads t ON t.id=j.thread_id WHERE t.agent_id=$1 AND j.state IN ('pending','waiting','unknown'))
 UNION ALL SELECT 'instructions' WHERE EXISTS(SELECT 1 FROM runtime.instruction_preparations j JOIN runtime.threads t ON t.id=j.thread_id WHERE t.agent_id=$1 AND (j.state IN ('pending','waiting','unknown') OR NOT j.output_acknowledged))
 UNION ALL SELECT 'observers' WHERE EXISTS(SELECT 1 FROM runtime.observer_controls WHERE agent_id=$1 AND (desired='running' AND mode='continuous' OR state NOT IN ('completed','failed','cancelled')))
 UNION ALL SELECT 'observation_output' WHERE EXISTS(SELECT 1 FROM runtime.observation_sources WHERE agent_id=$1 AND (NOT closed OR kind IN ('mcp_connect','observe_command') AND confirmed_cursor<>cursor))
 UNION ALL SELECT 'worker_deliveries' WHERE EXISTS(SELECT 1 FROM runtime.thread_deliveries d JOIN runtime.thread_subscriptions s ON s.id=d.subscription_id WHERE s.agent_id=$1 AND d.state='pending' AND s.enabled AND s.generation=d.generation)
 UNION ALL SELECT 'observation_deliveries' WHERE EXISTS(SELECT 1 FROM runtime.observation_deliveries d JOIN runtime.subscriptions s ON s.id=d.subscription_id WHERE s.agent_id=$1 AND d.state='pending' AND s.enabled AND s.generation=d.generation)
 ) reasons ORDER BY reason`, agent)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		if err = rows.Scan(&reason); err != nil {
			return value, err
		}
		value.Busy = append(value.Busy, reason)
	}
	return value, rows.Err()
}

func (s *Store) AgentRunState(ctx context.Context, scope managedruntime.Scope) (managedruntime.AgentRunState, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return managedruntime.AgentRunState{}, err
	}
	defer rollback(tx)
	if !validScope(scope) {
		return managedruntime.AgentRunState{}, managedruntime.ErrInvalid
	}
	if err = runtimePurgeGate(ctx, tx, scope.FleetID, scope.AgentID); err != nil {
		return managedruntime.AgentRunState{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.agents WHERE id=$1)`, scope.AgentID).Scan(&exists); err != nil {
		return managedruntime.AgentRunState{}, err
	}
	if !exists {
		return managedruntime.AgentRunState{AgentID: scope.AgentID, Mode: "running", Version: 1, Busy: []string{}}, tx.Commit(ctx)
	}
	if err = checkScope(ctx, tx, scope); err != nil {
		return managedruntime.AgentRunState{}, err
	}
	result, err := agentRunState(ctx, tx, scope.AgentID)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) ChangeAgentLifecycle(ctx context.Context, scope managedruntime.Scope, change managedruntime.AgentLifecycleChange) (managedruntime.AgentLifecycleReceipt, error) {
	result := managedruntime.AgentLifecycleReceipt{RequestID: change.RequestID, Action: change.Action}
	if !validScope(scope) || change.Validate() != nil {
		return result, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = lockAgentAdmission(ctx, tx, scope); err != nil {
		return result, err
	}
	result, err = changeAgentLifecycle(ctx, tx, scope, change)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func changeAgentLifecycle(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, change managedruntime.AgentLifecycleChange) (managedruntime.AgentLifecycleReceipt, error) {
	result := managedruntime.AgentLifecycleReceipt{RequestID: change.RequestID, Action: change.Action}
	encoded, _ := json.Marshal(change)
	var previous []byte
	var matches bool
	err := tx.QueryRow(ctx, `SELECT result,request=$4::jsonb FROM runtime.lifecycle_receipts WHERE agent_id=$1 AND actor_id=$2 AND request_id=$3`, scope.AgentID, scope.ActorID, change.RequestID, encoded).Scan(&previous, &matches)
	if err == nil {
		if !matches {
			return result, managedruntime.ErrConflict
		}
		err = json.Unmarshal(previous, &result)
		return result, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	result.State, err = agentRunState(ctx, tx, scope.AgentID)
	if err != nil {
		return result, err
	}
	if result.State.Version != change.Version {
		return result, managedruntime.ErrConflict
	}
	result.Outcome = "applied"
	if change.Action != "resume" && !change.Interrupt && len(result.State.Busy) > 0 {
		result.Outcome = "deferred"
	} else {
		mode := "running"
		if change.Action == "pause" {
			mode = "paused"
		}
		invalidate := result.State.Mode != mode || change.Action == "restart"
		// Even a no-op resume orders future requests. Otherwise a delayed
		// pause with the old version could commit after this explicit intent.
		err = tx.QueryRow(ctx, `UPDATE runtime.agents SET run_mode=$2,lifecycle_version=lifecycle_version+1,
 epoch=epoch+CASE WHEN $3 THEN 1 ELSE 0 END,holder=CASE WHEN $3 THEN '' ELSE holder END,
 lease_until=CASE WHEN $3 THEN '-infinity'::timestamptz ELSE lease_until END
 WHERE id=$1 RETURNING lifecycle_version,epoch`, scope.AgentID, mode, invalidate).Scan(&result.State.Version, &result.State.ActivationEpoch)
		if err != nil {
			return result, err
		}
		result.State.Mode = mode
	}
	receipt, _ := json.Marshal(result)
	_, err = tx.Exec(ctx, `INSERT INTO runtime.lifecycle_receipts(agent_id,actor_id,request_id,request,result) VALUES($1,$2,$3,$4,$5)`, scope.AgentID, scope.ActorID, change.RequestID, encoded, receipt)
	return result, err
}
