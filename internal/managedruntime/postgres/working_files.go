package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func readWorkingFiles(ctx context.Context, tx pgx.Tx, thread string) (*managedruntime.WorkingFiles, error) {
	var value managedruntime.WorkingFiles
	err := tx.QueryRow(ctx, `SELECT location FROM runtime.thread_working_files WHERE thread_id=$1`, thread).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, value.Validate()
}

func (s *Store) BindWorkingFiles(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work, value managedruntime.WorkingFiles) (managedruntime.WorkingFiles, error) {
	if err := value.Validate(); err != nil {
		return value, err
	}
	if lease.AgentID != work.Scope.AgentID || !work.Scope.Capabilities.Allows(agentpolicy.Files) || !work.Scope.Capabilities.Allows(agentpolicy.WorkingFiles) || !work.Config.Capabilities.Allows(agentpolicy.Files) || !work.Config.Capabilities.Allows(agentpolicy.WorkingFiles) {
		return value, managedruntime.ErrDenied
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return value, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return value, err
	}
	if err := checkScope(ctx, tx, work.Scope); err != nil {
		return value, err
	}
	thread, err := readThread(ctx, tx, lease.AgentID, work.ThreadID)
	if err != nil {
		return value, err
	}
	if thread.Application == "memory" || thread.Retention != "active" {
		return value, managedruntime.ErrDenied
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.turns t JOIN runtime.inputs i ON i.id=t.input_id WHERE t.id=$1 AND t.thread_id=$2 AND t.activation_epoch=$3 AND t.state='running' AND i.state='active')`, work.TurnID, work.ThreadID, lease.Epoch).Scan(&active); err != nil {
		return value, err
	}
	if !active {
		return value, managedruntime.ErrConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.thread_working_files(thread_id,location) VALUES($1,$2) ON CONFLICT(thread_id) DO NOTHING`, thread.ID, value); err != nil {
		return value, err
	}
	stored, err := readWorkingFiles(ctx, tx, thread.ID)
	if err != nil {
		return value, err
	}
	return *stored, tx.Commit(ctx)
}
