package postgres

import (
	"context"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) DeleteThread(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.ThreadDeletionReceipt, error) {
	result := managedruntime.ThreadDeletionReceipt{ThreadID: id}
	if !validScope(scope) || id == "" {
		return result, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = checkScope(ctx, tx, scope); err != nil {
		return result, err
	}
	if err = threadGraph(ctx, tx, scope.AgentID); err != nil {
		return result, err
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.thread_deletions WHERE thread_id=$1 AND agent_id=$2 AND tenant_id=$3 AND user_id=$4 AND fleet_id=$5)`, id, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&result.Deleted); err != nil {
		return result, classify(err)
	}
	if result.Deleted {
		return result, tx.Commit(ctx)
	}
	thread, err := readThread(ctx, tx, scope.AgentID, id)
	if err != nil {
		return result, err
	}
	if thread.Kind != "worker" || thread.Retention != "archived" || thread.Application != "" {
		return result, managedruntime.ErrConflict
	}
	// Source ingestion and ACK can settle independently of the Thread. Lock the
	// original sources before checking their final responsibility for output.
	if _, err = tx.Exec(ctx, `SELECT id FROM runtime.observation_sources WHERE thread_id=$1 ORDER BY id FOR UPDATE`, id); err != nil {
		return result, err
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM runtime.threads WHERE parent_id=$1) OR
 EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state IN ('queued','active')) OR
 EXISTS(SELECT 1 FROM runtime.turns WHERE thread_id=$1 AND state IN ('running','waiting')) OR
 EXISTS(SELECT 1 FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1 AND a.state='started') OR
 EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1 AND (j.state IN ('pending','waiting','unknown') OR j.operation_live)) OR
 EXISTS(SELECT 1 FROM runtime.hooks WHERE thread_id=$1 AND state IN ('pending','waiting','unknown')) OR
 EXISTS(SELECT 1 FROM runtime.instruction_preparations WHERE thread_id=$1 AND (state IN ('pending','waiting','unknown') OR NOT output_acknowledged)) OR
 EXISTS(SELECT 1 FROM runtime.observer_controls WHERE thread_id=$1 AND (desired='running' AND mode='continuous' OR state NOT IN ('completed','failed','cancelled'))) OR
 EXISTS(SELECT 1 FROM runtime.observation_sources WHERE thread_id=$1 AND (NOT closed OR kind IN ('mcp_connect','observe_command') AND confirmed_cursor<>cursor)) OR
 EXISTS(SELECT 1 FROM runtime.observation_sources o JOIN runtime.subscriptions s ON s.agent_id=o.agent_id AND s.environment_id=o.environment_id AND s.operation_id=o.operation_id WHERE o.thread_id=$1 AND o.stop_requested AND s.enabled AND s.thread_id<>$1) OR
 EXISTS(SELECT 1 FROM runtime.memory_evidence e JOIN runtime.inputs i ON i.id=e.input_id WHERE i.thread_id=$1) OR
 EXISTS(SELECT 1 FROM runtime.notification_outbox WHERE thread_id=$1 AND state='pending') OR
 EXISTS(SELECT 1 FROM runtime.application_jobs WHERE thread_id=$1) OR
 EXISTS(SELECT 1 FROM runtime.main_triggers WHERE thread_id=$1) OR
 EXISTS(SELECT 1 FROM runtime.thread_deliveries d JOIN runtime.thread_subscriptions s ON s.id=d.subscription_id WHERE (s.worker_id=$1 OR s.thread_id=$1) AND d.state='pending' AND s.enabled AND s.generation=d.generation) OR
 EXISTS(SELECT 1 FROM runtime.observation_deliveries d JOIN runtime.subscriptions s ON s.id=d.subscription_id JOIN runtime.observation_sources o ON o.agent_id=s.agent_id AND o.environment_id=s.environment_id AND o.operation_id=s.operation_id WHERE o.thread_id=$1 AND d.state='pending' AND s.enabled AND s.generation=d.generation)`, id).Scan(&busy); err != nil {
		return result, err
	}
	if busy {
		return result, managedruntime.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO runtime.thread_deletions(thread_id,agent_id,tenant_id,user_id,fleet_id,request_id,actor_id) SELECT id,agent_id,$2,$3,$4,request_id,$5 FROM runtime.threads WHERE id=$1`, id, scope.TenantID, scope.UserID, scope.FleetID, scope.ActorID); err != nil {
		return result, err
	}
	// Delete only records owned by this Thread. Accepted inputs in other Threads,
	// usage accounting, observations, application data and Execution artifacts
	// retain their independent lifetimes.
	for _, query := range []string{
		`DELETE FROM runtime.instruction_preparations WHERE thread_id=$1`,
		`DELETE FROM runtime.hooks WHERE thread_id=$1`,
		`DELETE FROM runtime.thread_deliveries WHERE subscription_id IN (SELECT id FROM runtime.thread_subscriptions WHERE thread_id=$1 OR worker_id=$1)`,
		`DELETE FROM runtime.thread_subscriptions WHERE thread_id=$1 OR worker_id=$1`,
		`DELETE FROM runtime.observation_deliveries WHERE subscription_id IN (SELECT id FROM runtime.subscriptions WHERE thread_id=$1)`,
		`DELETE FROM runtime.subscription_actions WHERE subscription_id IN (SELECT id FROM runtime.subscriptions WHERE thread_id=$1) OR action_id IN (SELECT j.id FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1)`,
		`DELETE FROM runtime.subscriptions WHERE thread_id=$1`,
		`DELETE FROM runtime.observation_sources WHERE thread_id=$1`,
		`DELETE FROM runtime.observer_subscriptions WHERE thread_id=$1 OR control_id IN (SELECT id FROM runtime.observer_controls WHERE thread_id=$1)`,
		`DELETE FROM runtime.observer_controls WHERE thread_id=$1`,
		`DELETE FROM runtime.thread_actions WHERE action_id IN (SELECT j.id FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1)`,
		`DELETE FROM runtime.context_checkpoints WHERE thread_id=$1`,
		`DELETE FROM runtime.compactions WHERE thread_id=$1`,
		`DELETE FROM runtime.tools WHERE turn_id IN (SELECT id FROM runtime.turns WHERE thread_id=$1)`,
		`DELETE FROM runtime.attempts WHERE turn_id IN (SELECT id FROM runtime.turns WHERE thread_id=$1)`,
		`DELETE FROM runtime.turns WHERE thread_id=$1`,
		`DELETE FROM runtime.events WHERE thread_id=$1`,
		`DELETE FROM runtime.inputs WHERE thread_id=$1`,
		`DELETE FROM runtime.threads WHERE id=$1`,
	} {
		if _, err = tx.Exec(ctx, query, id); err != nil {
			return result, err
		}
	}
	result.Deleted = true
	return result, tx.Commit(ctx)
}
