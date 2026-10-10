package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func lockIdleApplicationSource(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, job managedruntime.ApplicationJob) error {
	if job.Application != "memory" {
		return managedruntime.ErrInvalid
	}
	// Match collaboration's order: a source Worker can receive a message from
	// Main while this transaction creates the application Worker beneath Main.
	if _, err := tx.Exec(ctx, `SELECT id FROM runtime.threads WHERE agent_id=$1 AND (id=$2 OR kind='main') ORDER BY id FOR UPDATE`, scope.AgentID, job.IdleSourceThread); err != nil {
		return classify(err)
	}
	var active, idle bool
	err := tx.QueryRow(ctx, `SELECT retention='active' AND application='',
 state='idle' AND updated_at<=clock_timestamp()-interval '60 seconds'
 AND NOT EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=t.id AND state IN ('queued','active','held'))
 AND NOT EXISTS(SELECT 1 FROM runtime.turns WHERE thread_id=t.id AND state IN ('running','waiting'))
 AND NOT EXISTS(SELECT 1 FROM runtime.tools tool JOIN runtime.turns turn ON turn.id=tool.turn_id WHERE turn.thread_id=t.id AND (tool.state IN ('pending','waiting','unknown') OR tool.operation_live))
 FROM runtime.threads t WHERE id=$1 AND agent_id=$2`, job.IdleSourceThread, scope.AgentID).Scan(&active, &idle)
	if err != nil {
		return classify(err)
	}
	if !active {
		return managedruntime.ErrDenied
	}
	if !idle {
		return managedruntime.ErrSourceBusy
	}
	return nil
}
