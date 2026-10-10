package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) Status(ctx context.Context, scope managedruntime.Scope) (managedruntime.RuntimeStatus, error) {
	result := managedruntime.RuntimeStatus{States: map[string]int64{}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp(),EXISTS(SELECT 1 FROM runtime.agents WHERE id=$1)`, scope.AgentID).Scan(&result.ObservedAt, &result.Initialized); err != nil {
		return result, err
	}
	if !result.Initialized {
		return result, tx.Commit(ctx)
	}
	if err := checkScope(ctx, tx, scope); err != nil {
		return result, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE retention='active'),count(*) FILTER(WHERE retention='archived'),COALESCE(max(id::text) FILTER(WHERE kind='main'),''),max(updated_at) FROM runtime.threads WHERE agent_id=$1`, scope.AgentID).Scan(&result.ActiveThreads, &result.ArchivedThreads, &result.MainThreadID, &result.LastActivity); err != nil {
		return result, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE i.state IN ('queued','active')),count(*) FILTER(WHERE i.state='held') FROM runtime.inputs i JOIN runtime.threads t ON t.id=i.thread_id WHERE t.agent_id=$1`, scope.AgentID).Scan(&result.PendingInputs, &result.HeldInputs); err != nil {
		return result, err
	}
	// A persisted running state can outlive its activation. Do not present a
	// stopped or superseded holder as confirmed current processing.
	rows, err := tx.Query(ctx, `SELECT CASE WHEN th.state='running' AND NOT EXISTS(
SELECT 1 FROM runtime.turns t JOIN runtime.agents a ON a.id=th.agent_id
WHERE t.thread_id=th.id AND t.state='running' AND t.generation=th.generation
AND t.activation_epoch=a.epoch AND a.lease_until>transaction_timestamp()
) THEN 'unconfirmed' ELSE th.state END AS state,count(*)
FROM runtime.threads th WHERE th.agent_id=$1 AND th.retention='active' GROUP BY 1`, scope.AgentID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return result, err
		}
		result.States[state] = count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
