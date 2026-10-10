package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func trackInput(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, thread managedruntime.Thread, input string, source managedruntime.InputSource) error {
	if source.Kind != "" || thread.Application != "" || !scope.Capabilities.Allows(agentpolicy.InputTracking) {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM runtime.input_tracking i JOIN runtime.threads t ON t.id=i.thread_id WHERE t.id=$1 AND i.scope_id=t.input_scope AND i.checked_at IS NULL`, thread.ID).Scan(&count); err != nil {
		return err
	}
	if count >= managedruntime.MaxTrackedInputs {
		return fmt.Errorf("%w: input checklist is full; confirm handled inputs or explicitly start a new context", managedruntime.ErrConflict)
	}
	_, err := tx.Exec(ctx, `INSERT INTO runtime.input_tracking(thread_id,input_id,scope_id,accepted_order) SELECT id,$2,input_scope,sequence FROM runtime.threads WHERE id=$1`, thread.ID, input)
	return err
}

func deliverTrackedInput(ctx context.Context, tx pgx.Tx, thread, input string) error {
	_, err := tx.Exec(ctx, `UPDATE runtime.input_tracking SET delivery='delivered',message_id=input_id WHERE thread_id=$1 AND input_id=$2 AND delivery='registered'`, thread, input)
	return err
}

func readInputReminders(ctx context.Context, tx pgx.Tx, thread string) ([]managedruntime.InputReminder, error) {
	// Fetch bounded previews, retaining block positions for original read_context
	// references. Large input bodies stay in the durable event store.
	rows, err := tx.Query(ctx, `SELECT i.input_id,jsonb_build_object('id',e.data->'id','blocks',(
	 SELECT jsonb_agg(CASE WHEN b->>'type'='text' THEN jsonb_build_object('type','text','text',left(b->>'text',256))
	 ELSE jsonb_build_object('type',b->'type','media',b->'media') END ORDER BY n)
	 FROM jsonb_array_elements(e.data->'blocks') WITH ORDINALITY AS blocks(b,n)))
	 FROM runtime.input_tracking i JOIN runtime.threads t ON t.id=i.thread_id JOIN runtime.events e ON e.thread_id=i.thread_id AND e.kind='message.appended' AND e.data->>'id'=i.message_id::text WHERE i.thread_id=$1 AND i.scope_id=t.input_scope AND i.checked_at IS NULL AND i.delivery='delivered' ORDER BY i.accepted_order,i.input_id`, thread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []managedruntime.InputReminder
	for rows.Next() {
		var item managedruntime.InputReminder
		var raw []byte
		if err = rows.Scan(&item.InputID, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &item.Message); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) InputChecks(ctx context.Context, scope managedruntime.Scope, thread string, query managedruntime.InputCheckQuery) (managedruntime.InputCheckPage, error) {
	result := managedruntime.InputCheckPage{Enabled: scope.Capabilities.Allows(agentpolicy.InputTracking), Items: []managedruntime.InputCheck{}}
	if err := query.Validate(); err != nil {
		return result, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return result, err
	}
	if err := tx.QueryRow(ctx, `SELECT input_scope FROM runtime.threads WHERE id=$1 AND agent_id=$2`, thread, scope.AgentID).Scan(&result.ScopeID); err != nil {
		return result, classify(err)
	}
	// Historical checks remain inspectable when the module is disabled. No row
	// means untracked, never an inferred completion or failure.
	rows, err := tx.Query(ctx, `SELECT `+inputCheckColumns+` FROM runtime.input_tracking i WHERE i.thread_id=$1 AND (i.message_id=ANY($2::uuid[]) OR i.message_id IS NULL AND i.input_id=ANY($2::uuid[])) ORDER BY i.accepted_order,i.input_id`, thread, query.MessageIDs)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		item, scanErr := scanInputCheck(rows)
		if scanErr != nil {
			rows.Close()
			return result, scanErr
		}
		result.Items = append(result.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func checkInputs(ctx context.Context, tx pgx.Tx, work managedruntime.ToolWork, action managedruntime.ThreadStateAction) (json.RawMessage, error) {
	if len(action.InputIDs) < 1 || len(action.InputIDs) > managedruntime.MaxTrackedInputs || action.ID != "" || action.Instructions != nil || action.Content != nil || action.Title != nil || action.Description != nil || action.Acceptance != nil || action.Status != nil || action.StatusReason != nil || action.Priority != nil {
		return nil, managedruntime.ErrInvalid
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range action.InputIDs {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return nil, managedruntime.ErrInvalid
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	var other bool
	var message string
	if err := tx.QueryRow(ctx, `SELECT a.id,EXISTS(SELECT 1 FROM runtime.tools other WHERE other.attempt_id=j.attempt_id AND other.call->>'tool_name'<>'check_inputs') FROM runtime.tools j JOIN runtime.attempts a ON a.id=j.attempt_id WHERE j.id=$1`, work.ID).Scan(&message, &other); err != nil {
		return nil, err
	}
	if other {
		return nil, fmt.Errorf("%w: check_inputs must be in a response containing no other tools; wait for their results first", managedruntime.ErrConflict)
	}
	var eligible int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM runtime.input_tracking i JOIN runtime.threads t ON t.id=i.thread_id WHERE i.thread_id=$1 AND i.input_id=ANY($2::uuid[]) AND i.scope_id=t.input_scope AND i.delivery='delivered'`, work.ThreadID, ids).Scan(&eligible); err != nil {
		return nil, err
	}
	if eligible != len(ids) {
		return nil, fmt.Errorf("%w: every input must be delivered in this Thread's current scope", managedruntime.ErrInvalid)
	}
	now := time.Now().UTC()
	changed := []string{}
	for _, id := range ids {
		tag, err := tx.Exec(ctx, `UPDATE runtime.input_tracking SET checked_at=$3,check_action_id=$4,check_message_id=$5,tool_use_id=$6 WHERE thread_id=$1 AND input_id=$2 AND checked_at IS NULL`, work.ThreadID, id, now, work.ID, message, work.Call.ToolUseID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			changed = append(changed, id)
		}
	}
	if len(changed) > 0 {
		if err := appendEvent(ctx, tx, work.ThreadID, "input.checked", map[string]any{"input_ids": changed, "checked_at": now, "action_id": work.ID, "message_id": message, "tool_use_id": work.Call.ToolUseID}); err != nil {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"checked_input_ids": ids})
}

func scanInputCheck(row scanner) (managedruntime.InputCheck, error) {
	var item managedruntime.InputCheck
	err := row.Scan(&item.InputID, &item.ScopeID, &item.MessageID, &item.AcceptedOrder, &item.Delivery, &item.CheckedAt, &item.CheckActionID, &item.CheckMessageID, &item.ToolUseID)
	return item, err
}

const inputCheckColumns = `i.input_id,i.scope_id,COALESCE(i.message_id::text,''),i.accepted_order,i.delivery,i.checked_at,COALESCE(i.check_action_id::text,''),COALESCE(i.check_message_id::text,''),COALESCE(i.tool_use_id,'')`

func readInputChecklist(ctx context.Context, tx pgx.Tx, thread string, enabled bool) (managedruntime.InputChecklist, error) {
	result := managedruntime.InputChecklist{Enabled: enabled, Items: []managedruntime.InputCheck{}}
	if err := tx.QueryRow(ctx, `SELECT input_scope FROM runtime.threads WHERE id=$1`, thread).Scan(&result.ScopeID); err != nil {
		return result, err
	}
	if !enabled {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+inputCheckColumns+` FROM runtime.input_tracking i WHERE i.thread_id=$1 AND i.scope_id=$2 AND i.checked_at IS NULL ORDER BY i.accepted_order,i.input_id`, thread, result.ScopeID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanInputCheck(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}
