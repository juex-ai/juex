package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) WriteProgress(ctx context.Context, lease managedruntime.Lease, attempt string, snapshot managedruntime.ProgressSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
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
	if err := tx.QueryRow(ctx, `SELECT t.thread_id FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id JOIN runtime.threads th ON th.id=t.thread_id WHERE a.id=$1 AND th.agent_id=$2`, attempt, lease.AgentID).Scan(&thread); err != nil {
		return classify(err)
	}
	if _, err := readThread(ctx, tx, lease.AgentID, thread); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT a.state='started' AND t.state='running' AND t.activation_epoch=$2 AND (a.request->>'generation')::bigint=th.generation FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id JOIN runtime.threads th ON th.id=t.thread_id WHERE a.id=$1`, attempt, lease.Epoch).Scan(&active); err != nil {
		return classify(err)
	}
	if !active {
		return managedruntime.ErrConflict
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	// A completed attempt deletes its projection under the same Thread lock.
	// Updating an existing row cannot recreate it after final settlement.
	if _, err := tx.Exec(ctx, `UPDATE runtime.attempt_progress SET revision=$2,snapshot=$3,updated_at=clock_timestamp() WHERE attempt_id=$1 AND revision<$2`, attempt, snapshot.Revision, encoded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readProgress(ctx context.Context, tx pgx.Tx, thread managedruntime.Thread) ([]managedruntime.ModelProgress, error) {
	rows, err := tx.Query(ctx, `SELECT a.id,a.turn_id,(a.request->>'generation')::bigint,p.start_sequence,(a.request->'model'->>'provider')||':'||(a.request->'model'->>'model'),
CASE WHEN t.state='cancelled' THEN 'cancelled' WHEN a.state='started' AND (ag.epoch<>t.activation_epoch OR ag.lease_until<=clock_timestamp()) THEN 'unknown' WHEN a.state='started' THEN 'running' ELSE a.state END,
a.started_at,p.updated_at,p.snapshot
FROM runtime.attempt_progress p JOIN runtime.attempts a ON a.id=p.attempt_id JOIN runtime.turns t ON t.id=a.turn_id JOIN runtime.agents ag ON ag.id=$3
WHERE t.thread_id=$1 AND (a.request->>'generation')::bigint=$2 AND p.revision>0 ORDER BY a.started_at DESC,a.ordinal DESC LIMIT 4`, thread.ID, thread.Generation, thread.AgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []managedruntime.ModelProgress{}
	for rows.Next() {
		var value managedruntime.ModelProgress
		var snapshot []byte
		if err := rows.Scan(&value.AttemptID, &value.TurnID, &value.Generation, &value.Sequence, &value.Model, &value.State, &value.StartedAt, &value.UpdatedAt, &snapshot); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(snapshot, &value.Snapshot); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
