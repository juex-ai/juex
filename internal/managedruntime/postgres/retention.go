package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// Graph mutations serialize before taking Thread locks. Normal Turn execution
// does not acquire this lock and retains independent concurrency.
func threadGraph(ctx context.Context, tx pgx.Tx, agent string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,5107))`, agent)
	return err
}

func (s *Store) SetThreadArchived(ctx context.Context, scope managedruntime.Scope, id string, archived bool) (managedruntime.Thread, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.Thread{}, err
	}
	defer rollback(tx)
	if err = checkScope(ctx, tx, scope); err != nil {
		return managedruntime.Thread{}, err
	}
	if err = threadGraph(ctx, tx, scope.AgentID); err != nil {
		return managedruntime.Thread{}, err
	}
	thread, err := readThread(ctx, tx, scope.AgentID, id)
	if err != nil {
		return thread, err
	}
	thread, err = setThreadArchived(ctx, tx, scope, thread, archived)
	if err != nil {
		return thread, err
	}
	return thread, tx.Commit(ctx)
}

func setThreadArchived(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, thread managedruntime.Thread, archived bool) (managedruntime.Thread, error) {
	id := thread.ID
	var err error
	if thread.Kind != "worker" {
		return thread, managedruntime.ErrInvalid
	}
	retention := "active"
	if archived {
		retention = "archived"
	}
	if thread.Retention == retention {
		return thread, nil
	}
	if archived {
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state IN ('queued','active')) OR
 EXISTS(SELECT 1 FROM runtime.turns WHERE thread_id=$1 AND state IN ('running','waiting')) OR
 EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1 AND (j.state IN ('pending','waiting','unknown') OR j.operation_live)) OR
 EXISTS(SELECT 1 FROM runtime.observer_controls WHERE thread_id=$1 AND (desired='running' AND mode='continuous' OR state NOT IN ('completed','failed','cancelled'))) OR
 EXISTS(SELECT 1 FROM runtime.hooks WHERE thread_id=$1 AND state IN ('pending','waiting','unknown')) OR
 EXISTS(SELECT 1 FROM runtime.instruction_preparations WHERE thread_id=$1 AND (state IN ('pending','waiting','unknown') OR NOT output_acknowledged)) OR
 EXISTS(SELECT 1 FROM runtime.threads WHERE parent_id=$1 AND retention='active') OR
 EXISTS(SELECT 1 FROM runtime.thread_deliveries d JOIN runtime.thread_subscriptions s ON s.id=d.subscription_id WHERE s.worker_id=$1 AND d.state='pending' AND s.enabled AND s.generation=d.generation)`, id).Scan(&busy); err != nil {
			return thread, err
		}
		if busy {
			return thread, managedruntime.ErrConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime.observer_subscriptions SET enabled=false WHERE thread_id=$1`, id); err != nil {
			return thread, err
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime.subscriptions SET enabled=false,generation=generation+1 WHERE thread_id=$1 AND enabled`, id); err != nil {
			return thread, err
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime.thread_subscriptions SET enabled=false,generation=generation+1 WHERE (thread_id=$1 OR worker_id=$1) AND enabled`, id); err != nil {
			return thread, err
		}
	} else {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT retention='active' FROM runtime.threads WHERE id=$1`, thread.ParentID).Scan(&active); err != nil {
			return thread, err
		}
		if !active {
			return thread, managedruntime.ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime.threads SET retention=$2 WHERE id=$1`, id, retention); err != nil {
		return thread, err
	}
	if err = appendEvent(ctx, tx, id, "thread.retention", map[string]any{"retention": retention, "actor_id": scope.ActorID}); err != nil {
		return thread, err
	}
	thread, err = readThread(ctx, tx, scope.AgentID, id)
	if err != nil {
		return thread, err
	}
	return thread, nil
}
