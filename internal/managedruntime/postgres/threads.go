package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) CreateWorker(ctx context.Context, scope managedruntime.Scope, parentID, requestID, name string) (managedruntime.Thread, error) {
	if !validScope(scope) || parentID == "" || requestID == "" || len(requestID) > 200 || strings.TrimSpace(name) == "" || len([]rune(name)) > 100 {
		return managedruntime.Thread{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.Thread{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.Thread{}, err
	}
	if err := threadGraph(ctx, tx, scope.AgentID); err != nil {
		return managedruntime.Thread{}, err
	}
	parent, err := readThread(ctx, tx, scope.AgentID, parentID)
	if err != nil {
		return parent, err
	}
	thread, err := createWorker(ctx, tx, scope, parent, requestID, name, scope.WorkerDepth)
	if err != nil {
		return thread, err
	}
	return thread, tx.Commit(ctx)
}

func createWorker(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, parent managedruntime.Thread, requestID, name string, maxDepth int) (managedruntime.Thread, error) {
	if strings.TrimSpace(name) == "" || len([]rune(name)) > 100 {
		return managedruntime.Thread{}, managedruntime.ErrInvalid
	}
	if maxDepth == 0 {
		maxDepth = 1
	}
	if maxDepth < 1 || maxDepth > 2 {
		return managedruntime.Thread{}, managedruntime.ErrInvalid
	}
	parentID := parent.ID
	var depth int
	if err := tx.QueryRow(ctx, `WITH RECURSIVE parents AS (SELECT id,parent_id,0 AS depth FROM runtime.threads WHERE id=$1 UNION ALL SELECT t.id,t.parent_id,p.depth+1 FROM runtime.threads t JOIN parents p ON t.id=p.parent_id) SELECT max(depth) FROM parents`, parentID).Scan(&depth); err != nil {
		return managedruntime.Thread{}, err
	}
	if depth >= maxDepth {
		return managedruntime.Thread{}, managedruntime.ErrInvalid
	}
	if parent.Retention != "active" {
		return managedruntime.Thread{}, managedruntime.ErrDenied
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO runtime.threads(agent_id,parent_id,kind,name,request_id) VALUES($1,$2,'worker',$3,$4) ON CONFLICT(agent_id,request_id) DO NOTHING RETURNING id`, scope.AgentID, parentID, name, requestID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM runtime.threads WHERE agent_id=$1 AND request_id=$2 AND parent_id=$3 AND name=$4`, scope.AgentID, requestID, parentID, name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return managedruntime.Thread{}, managedruntime.ErrConflict
		}
	} else if err == nil {
		err = appendEvent(ctx, tx, id, "thread.created", map[string]string{"kind": "worker", "parent_id": parentID, "actor_id": scope.ActorID})
	}
	if err != nil {
		return managedruntime.Thread{}, classify(err)
	}
	// On an idempotent retry the child already exists. Taking its row lock
	// after the parent would invert the UUID order used by collaboration.
	thread, err := scanThread(tx.QueryRow(ctx, `SELECT `+threadColumns+` FROM runtime.threads t WHERE t.agent_id=$1 AND t.id=$2`, scope.AgentID, id))
	if err != nil {
		return thread, err
	}
	return thread, nil
}

// Cancellation is a durable user command, independent of an Activation's RAM.
// It affects only this Thread. Provider cancellation is best effort; its final
// accounting may arrive later and must not append a cancelled assistant reply.
func (s *Store) CancelThread(ctx context.Context, scope managedruntime.Scope, threadID string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return err
	}
	thread, err := readThread(ctx, tx, scope.AgentID, threadID)
	if err != nil {
		return err
	}
	if err := cancelThread(ctx, tx, scope, thread); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cancelThread(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, thread managedruntime.Thread) error {
	if thread.Retention != "active" {
		return managedruntime.ErrDenied
	}
	rows, err := tx.Query(ctx, `SELECT id FROM runtime.turns WHERE thread_id=$1 AND state IN ('running','waiting')`, thread.ID)
	if err != nil {
		return err
	}
	var turns []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		turns = append(turns, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range turns {
		if err := threadResult(ctx, tx, thread.ID, id, "cancelled", ""); err != nil {
			return err
		}
		if err := cancelCompaction(ctx, tx, id); err != nil {
			return err
		}
		if err := consumeToolResults(ctx, tx, id, thread.ID, true); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='cancelled' WHERE thread_id=$1 AND state IN ('queued','active');`, thread.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.thread_subscriptions SET enabled=false,generation=generation+1 WHERE thread_id=$1 AND enabled`, thread.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.subscriptions SET enabled=false,generation=generation+1 WHERE thread_id=$1 AND enabled`, thread.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='cancelled',completed_at=clock_timestamp() WHERE thread_id=$1 AND state IN ('running','waiting')`, thread.ID); err != nil {
		return err
	}
	// A completed Turn can still own a live shell/MCP handle. Cancel its
	// delivery independently without rewriting completed conversation history.
	if _, err := tx.Exec(ctx, `UPDATE runtime.tools j SET cancel_requested=true,next_check=clock_timestamp(),wake_version=wake_version+1 FROM runtime.turns t WHERE t.id=j.turn_id AND t.thread_id=$1 AND (j.state IN ('pending','waiting') OR j.operation_live)`, thread.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='idle' WHERE id=$1`, thread.ID); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread.ID, "thread.cancelled", map[string]string{"actor_id": scope.ActorID}); err != nil {
		return err
	}
	return nil
}

func (s *Store) WorkActive(ctx context.Context, lease managedruntime.Lease, turnID string) (bool, error) {
	var active bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.turns t JOIN runtime.threads th ON th.id=t.thread_id JOIN runtime.agents a ON a.id=th.agent_id WHERE t.id=$1 AND a.id=$2 AND a.holder=$3 AND a.epoch=$4 AND a.lease_until>clock_timestamp() AND t.activation_epoch=$4 AND t.state='running')`, turnID, lease.AgentID, lease.Holder, lease.Epoch).Scan(&active)
	return active, err
}
