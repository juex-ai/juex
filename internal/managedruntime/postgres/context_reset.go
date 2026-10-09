package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) ResetContext(ctx context.Context, scope managedruntime.Scope, threadID, requestID string) (managedruntime.Thread, error) {
	if !validScope(scope) || threadID == "" || strings.TrimSpace(requestID) == "" || len(requestID) > 200 {
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
	thread, err := readThread(ctx, tx, scope.AgentID, threadID)
	if err != nil {
		return thread, err
	}
	if thread.Retention != "active" || thread.Application != "" || thread.Kind == "worker" && !scope.Capabilities.Allows(agentpolicy.Workers) {
		return thread, managedruntime.ErrDenied
	}
	var receipt managedruntime.Thread
	err = tx.QueryRow(ctx, `SELECT result FROM runtime.context_resets WHERE thread_id=$1 AND request_id=$2`, thread.ID, requestID).Scan(&receipt)
	if err == nil {
		return receipt, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return thread, err
	}
	if thread.State != "idle" && thread.State != "failed" || thread.PendingInputs != 0 || thread.HeldInputs != 0 {
		return thread, managedruntime.ErrConflict
	}
	// Idle after cancellation does not prove an external effect has settled.
	// Already-delivered live connections remain valid across a context reset.
	var unsettled bool
	if err := tx.QueryRow(ctx, `SELECT
	 EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1 AND (NOT j.consumed OR j.state IN ('pending','waiting','unknown') OR j.cancel_requested AND j.operation_live)) OR
	 EXISTS(SELECT 1 FROM runtime.hooks WHERE thread_id=$1 AND state IN ('pending','waiting','unknown')) OR
	 EXISTS(SELECT 1 FROM runtime.instruction_preparations WHERE thread_id=$1 AND (state IN ('pending','waiting','unknown') OR NOT output_acknowledged))`, thread.ID).Scan(&unsettled); err != nil {
		return thread, err
	}
	if unsettled {
		return thread, managedruntime.ErrConflict
	}
	state, err := readThreadState(ctx, tx, thread.ID)
	if err != nil {
		return thread, err
	}
	state = state.RenewContext(scope.Capabilities.Allows(agentpolicy.Notes), scope.Capabilities.Allows(agentpolicy.Tasks))
	if err := writeThreadState(ctx, tx, thread.ID, state); err != nil {
		return thread, err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET generation=generation+1,state='idle' WHERE id=$1`, thread.ID); err != nil {
		return thread, err
	}
	if err := appendEvent(ctx, tx, thread.ID, "context.reset", map[string]any{"request_id": requestID, "revision": state.Revision}); err != nil {
		return thread, err
	}
	thread, err = readThread(ctx, tx, scope.AgentID, thread.ID)
	if err != nil {
		return thread, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.context_resets(thread_id,request_id,result) VALUES($1,$2,$3)`, thread.ID, requestID, thread); err != nil {
		return thread, err
	}
	return thread, tx.Commit(ctx)
}
