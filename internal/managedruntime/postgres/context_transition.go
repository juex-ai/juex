package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func requestContextTransition(ctx context.Context, tx pgx.Tx, work managedruntime.ToolWork, action managedruntime.ThreadStateAction) (json.RawMessage, error) {
	if action.ID != "" || action.Content != nil || action.Title != nil || action.Description != nil || action.Acceptance != nil || action.Status != nil || action.StatusReason != nil || action.Priority != nil {
		return nil, managedruntime.ErrInvalid
	}
	focus := ""
	if action.Instructions != nil {
		if action.Kind != "context_compact" || len(*action.Instructions) > 4096 || !utf8.ValidString(*action.Instructions) {
			return nil, managedruntime.ErrInvalid
		}
		focus = strings.TrimSpace(*action.Instructions)
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.context_transitions c JOIN runtime.tools j ON j.id=c.action_id WHERE j.turn_id=$1 AND NOT c.applied)`, work.TurnID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, managedruntime.ErrConflict
	}
	if action.Kind == "context_new" {
		blocked, err := resetWouldHideWork(ctx, tx, work.ThreadID)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, managedruntime.ErrConflict
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.context_transitions(action_id,kind,focus) VALUES($1,$2,$3)`, work.ID, action.Kind, focus); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"accepted": true, "action": action.Kind, "apply_after": "tool_batch"})
}

func resetWouldHideWork(ctx context.Context, tx pgx.Tx, thread string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state IN ('queued','held')) OR
	 EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1 AND (j.state='unknown' OR j.cancel_requested AND j.operation_live)) OR
	 EXISTS(SELECT 1 FROM runtime.hooks WHERE thread_id=$1 AND state IN ('pending','waiting','unknown')) OR
	 EXISTS(SELECT 1 FROM runtime.instruction_preparations WHERE thread_id=$1 AND (state IN ('pending','waiting','unknown') OR NOT output_acknowledged))`, thread).Scan(&blocked)
	return blocked, err
}

// The old tool_use and every result enter the old Generation before this
// boundary. The next request therefore never sees an orphaned tool result.
func applyContextTransition(ctx context.Context, tx pgx.Tx, turn, thread string) error {
	var action, kind, focus string
	var config managedruntime.TurnConfig
	err := tx.QueryRow(ctx, `SELECT c.action_id,c.kind,c.focus,t.config FROM runtime.context_transitions c JOIN runtime.tools j ON j.id=c.action_id JOIN runtime.turns t ON t.id=j.turn_id WHERE j.turn_id=$1 AND NOT c.applied`, turn).Scan(&action, &kind, &focus, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.context_transitions SET applied=true WHERE action_id=$1`, action); err != nil {
		return err
	}
	if kind == "context_compact" {
		sequence, err := contextSequence(ctx, tx, thread)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.compactions(turn_id,thread_id,source_generation,source_sequence,reason,focus) SELECT $1,id,generation,$3,'manual',$4 FROM runtime.threads WHERE id=$2`, turn, thread, sequence, focus); err != nil {
			return err
		}
		return appendEvent(ctx, tx, thread, "context.compacting", map[string]string{"turn_id": turn, "reason": "model", "tool_id": action})
	}
	blocked, err := resetWouldHideWork(ctx, tx, thread)
	if err != nil {
		return err
	}
	if blocked {
		message := llm.TextMessage(llm.RoleUser, "Context reset was not applied because another input or an unknown external outcome arrived. Current context is preserved; process the pending work before requesting context_new again.")
		message.ID, message.Kind = uuid.NewString(), llm.MessageKindSystemNotice
		return appendEvent(ctx, tx, thread, "message.appended", message)
	}
	state, err := readThreadState(ctx, tx, thread)
	if err != nil {
		return err
	}
	state = state.RenewContext(config.Capabilities.Allows(agentpolicy.Notes), config.Capabilities.Allows(agentpolicy.Tasks))
	if err := writeThreadState(ctx, tx, thread, state); err != nil {
		return err
	}
	var generation int64
	if err := tx.QueryRow(ctx, `UPDATE runtime.threads SET generation=generation+1 WHERE id=$1 RETURNING generation`, thread).Scan(&generation); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET generation=$2 WHERE id=$1`, turn, generation); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread, "context.reset", map[string]any{"tool_id": action, "revision": state.Revision}); err != nil {
		return err
	}
	message := llm.TextMessage(llm.RoleUser, "A new context was requested. The current Thread's unfinished Tasks remain authoritative. Continue them or report completion if no work remains.")
	message.ID, message.Kind = uuid.NewString(), llm.MessageKindSystemNotice
	return appendEvent(ctx, tx, thread, "message.appended", message)
}
