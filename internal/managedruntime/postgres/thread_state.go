package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func readThreadState(ctx context.Context, tx pgx.Tx, thread string) (managedruntime.ThreadState, error) {
	state := managedruntime.ThreadState{Tasks: []managedruntime.ThreadTask{}}
	var encoded []byte
	err := tx.QueryRow(ctx, `SELECT value FROM runtime.thread_state WHERE thread_id=$1`, thread).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err == nil {
		err = json.Unmarshal(encoded, &state)
	}
	return state, err
}

func writeThreadState(ctx context.Context, tx pgx.Tx, thread string, state managedruntime.ThreadState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO runtime.thread_state(thread_id,value) VALUES($1,$2) ON CONFLICT(thread_id) DO UPDATE SET value=EXCLUDED.value`, thread, state)
	return err
}

func (s *Store) ApplyThreadStateAction(ctx context.Context, work managedruntime.ToolWork, action managedruntime.ThreadStateAction) (json.RawMessage, error) {
	if !managedruntime.IsThreadStateTool(action.Kind) || action.Kind != work.Call.ToolName {
		return nil, managedruntime.ErrInvalid
	}
	required := agentpolicy.Tasks
	if action.Kind == "update_notes" {
		required = agentpolicy.Notes
	}
	if action.Kind == "context_new" || action.Kind == "context_compact" {
		required = agentpolicy.ContextControl
	}
	if !work.Scope.Capabilities.Allows(required) || !work.FrozenCapabilities.Allows(required) {
		return nil, managedruntime.ErrDenied
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, work.Scope); err != nil {
		return nil, err
	}
	thread, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID)
	if err != nil {
		return nil, err
	}
	if thread.Application == "memory" {
		return nil, managedruntime.ErrDenied
	}
	if err := subscriptionAction(ctx, tx, work); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	var previous json.RawMessage
	var matches bool
	err = tx.QueryRow(ctx, `SELECT result,request=$2::jsonb FROM runtime.thread_state_actions WHERE action_id=$1`, work.ID, encoded).Scan(&previous, &matches)
	if err == nil {
		if !matches {
			return nil, managedruntime.ErrConflict
		}
		return previous, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if required == agentpolicy.ContextControl {
		result, err := requestContextTransition(ctx, tx, work, action)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.thread_state_actions(action_id,request,result) VALUES($1,$2,$3)`, work.ID, encoded, result); err != nil {
			return nil, err
		}
		return result, tx.Commit(ctx)
	}
	state, err := readThreadState(ctx, tx, thread.ID)
	if err != nil {
		return nil, err
	}
	clearNotes := work.Scope.Capabilities.Allows(agentpolicy.Notes) && work.FrozenCapabilities.Allows(agentpolicy.Notes)
	state, result, err := state.Apply(action, uuid.NewString(), time.Now(), clearNotes)
	if err != nil {
		return nil, err
	}
	if action.Kind != "list_tasks" {
		if err := writeThreadState(ctx, tx, thread.ID, state); err != nil {
			return nil, err
		}
		if err := appendEvent(ctx, tx, thread.ID, "thread.state.updated", map[string]any{"tool_id": work.ID, "revision": state.Revision, "state": state}); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.thread_state_actions(action_id,request,result) VALUES($1,$2,$3)`, work.ID, encoded, result); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
