package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func completeTurn(ctx context.Context, tx pgx.Tx, thread, turn, input, state, text, failure string) error {
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state=$2,completed_at=clock_timestamp(),deferred_finish=NULL WHERE id=$1`, turn, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state=$2 WHERE id=$1`, input, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') THEN 'queued' WHEN $2='failed' THEN 'failed' ELSE 'idle' END WHERE id=$1`, thread, state); err != nil {
		return err
	}
	if err := threadResult(ctx, tx, thread, turn, state, text); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread, "turn."+state, map[string]string{"turn_id": turn, "input_id": input, "error": failure}); err != nil {
		return err
	}
	if state == "completed" {
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.memory_evidence(input_id) SELECT id FROM runtime.inputs WHERE id=$1 AND COALESCE(source->>'kind','')='' ON CONFLICT DO NOTHING`, input); err != nil {
			return err
		}
	}
	return nil
}

func waitHooks(ctx context.Context, tx pgx.Tx, turn, thread string, unknown bool) error {
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='waiting' WHERE id=$1`, turn); err != nil {
		return err
	}
	state := "waiting"
	if unknown {
		state = "blocked"
	}
	_, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=$2 WHERE id=$1`, thread, state)
	return err
}

func beginTurnHooks(ctx context.Context, tx pgx.Tx, work managedruntime.Work, text string) (bool, error) {
	var first string
	if err := tx.QueryRow(ctx, `UPDATE runtime.threads SET hook_start_turn=COALESCE(hook_start_turn,$2) WHERE id=$1 RETURNING hook_start_turn`, work.ThreadID, work.TurnID).Scan(&first); err != nil {
		return false, err
	}
	var events []hookpolicy.Event
	if first == work.TurnID {
		events = append(events, hookpolicy.ThreadStart)
	}
	if work.Source.Kind == "" {
		events = append(events, hookpolicy.UserPromptSubmit)
	}
	for _, event := range events {
		if err := enqueueHooks(ctx, tx, work.TurnID, event, work.InputID, managedruntime.HookInput{UserInput: managedruntime.HookText(text, 16<<10)}); err != nil {
			return false, err
		}
		decision, err := hookDecision(ctx, tx, work.TurnID, event, work.InputID)
		if err != nil {
			return false, err
		}
		if decision.Unknown || !decision.Ready {
			return true, waitHooks(ctx, tx, work.TurnID, work.ThreadID, decision.Unknown)
		}
		if decision.Reject {
			return true, completeTurn(ctx, tx, work.ThreadID, work.TurnID, work.InputID, "failed", "", decision.Reason)
		}
		if err := applyHookContext(ctx, tx, work.TurnID, work.ThreadID, event, work.InputID, decision); err != nil {
			return false, err
		}
	}
	return false, nil
}

type hookFinish struct {
	Event     hookpolicy.Event `json:"event"`
	Anchor    string           `json:"anchor"`
	FinalText string           `json:"final_text"`
	Resume    bool             `json:"resume"`
}

func deferHookFinish(ctx context.Context, tx pgx.Tx, turn, thread string, pending hookFinish) error {
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET deferred_finish=$2 WHERE id=$1`, turn, pending); err != nil {
		return err
	}
	return waitHooks(ctx, tx, turn, thread, false)
}

func resumeHookFinish(ctx context.Context, tx pgx.Tx, work *managedruntime.Work) (bool, error) {
	var pending *hookFinish
	var continuations int
	if err := tx.QueryRow(ctx, `SELECT deferred_finish,hook_continuations FROM runtime.turns WHERE id=$1`, work.TurnID).Scan(&pending, &continuations); err != nil {
		return false, err
	}
	if pending == nil {
		return false, nil
	}
	decision, err := hookDecision(ctx, tx, work.TurnID, pending.Event, pending.Anchor)
	if err != nil {
		return false, err
	}
	if decision.Unknown || !decision.Ready {
		return true, waitHooks(ctx, tx, work.TurnID, work.ThreadID, decision.Unknown)
	}
	if decision.Reject {
		return true, completeTurn(ctx, tx, work.ThreadID, work.TurnID, work.InputID, "failed", "", decision.Reason)
	}
	if decision.Continue && continuations >= 4 {
		return true, completeTurn(ctx, tx, work.ThreadID, work.TurnID, work.InputID, "failed", "", "Stop hook continuation limit reached")
	}
	if err := applyHookContext(ctx, tx, work.TurnID, work.ThreadID, pending.Event, pending.Anchor, decision); err != nil {
		return false, err
	}
	if decision.Continue || pending.Resume {
		if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET deferred_finish=NULL,hook_continuations=hook_continuations+CASE WHEN $2 THEN 1 ELSE 0 END WHERE id=$1`, work.TurnID, decision.Continue); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, completeTurn(ctx, tx, work.ThreadID, work.TurnID, work.InputID, "completed", pending.FinalText, "")
}
