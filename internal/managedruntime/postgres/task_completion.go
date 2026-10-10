package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// Called only after the normal Stop hooks settle, under the Thread lock. An
// imported task is inert until a new authorized Turn reaches this boundary.
func finishConversation(ctx context.Context, tx pgx.Tx, thread, turn, input, outcome, text, failure string) error {
	if outcome != "completed" || failure != "" {
		return completeTurn(ctx, tx, thread, turn, input, outcome, text, failure)
	}
	var config managedruntime.TurnConfig
	var application string
	if err := tx.QueryRow(ctx, `SELECT t.config,th.application FROM runtime.turns t JOIN runtime.threads th ON th.id=t.thread_id WHERE t.id=$1`, turn).Scan(&config, &application); err != nil {
		return err
	}
	if !config.Capabilities.Allows(agentpolicy.Tasks) || application == "memory" {
		return completeTurn(ctx, tx, thread, turn, input, outcome, text, failure)
	}
	state, err := readThreadState(ctx, tx, thread)
	if err != nil {
		return err
	}
	selected, ok := state.SelectedTask()
	if !ok {
		return completeTurn(ctx, tx, thread, turn, input, outcome, text, failure)
	}
	// A subscribed Worker's active work or undelivered result owns the next
	// wakeup. Completing this Turn avoids polling the provider while waiting.
	var waiting bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') OR EXISTS(SELECT 1 FROM runtime.thread_subscriptions s JOIN runtime.threads w ON w.id=s.worker_id WHERE s.thread_id=$1 AND s.enabled AND (
	 EXISTS(SELECT 1 FROM runtime.inputs i WHERE i.thread_id=w.id AND i.state IN ('queued','active')) OR
	 EXISTS(SELECT 1 FROM runtime.thread_deliveries d WHERE d.subscription_id=s.id AND d.generation=s.generation AND d.state='pending')))`, thread).Scan(&waiting); err != nil {
		return err
	}
	if waiting {
		return completeTurn(ctx, tx, thread, turn, input, outcome, text, failure)
	}
	state, err = state.ContinueTask(selected, time.Now())
	if err != nil {
		return err
	}
	if err := writeThreadState(ctx, tx, thread, state); err != nil {
		return err
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	message := llm.TextMessage(llm.RoleUser, "The current Thread still has unfinished work. Continue the selected task, verify its acceptance criteria, and update its status with update_task. If blocked, record pending or failed with a concrete reason. Do not report completion while the task remains todo or doing.\nSelected task:\n"+string(encoded))
	message.ID, message.Kind = uuid.NewString(), llm.MessageKindSystemNotice
	if err := appendEvent(ctx, tx, thread, "message.appended", message); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread, "tasks.continued", map[string]any{"turn_id": turn, "task_id": selected.ID, "continuation_count": selected.ContinuationCount + 1, "revision": state.Revision}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET deferred_finish=NULL,state='running' WHERE id=$1`, turn); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runtime.threads SET state='queued' WHERE id=$1`, thread)
	return err
}
