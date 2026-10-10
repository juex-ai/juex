package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// All participants are locked in UUID order before the tool fence is checked.
// This serializes acceptance with cancellation without an A -> B / B -> A
// deadlock. Once committed, the destination input owns its own lifetime.
func (s *Store) ApplyThreadAction(ctx context.Context, work managedruntime.ToolWork, action managedruntime.ThreadAction, target managedruntime.Scope) (json.RawMessage, error) {
	if !validScope(target) || target.ActorID != work.Scope.ActorID || target.TenantID != work.Scope.TenantID || target.UserID != work.Scope.UserID || target.FleetID != work.Scope.FleetID {
		return nil, managedruntime.ErrDenied
	}
	if action.Kind != "agent_send" && target.AgentID != work.Scope.AgentID {
		return nil, managedruntime.ErrDenied
	}
	if action.Kind == "agent_send" {
		if target.AgentID == work.Scope.AgentID || action.ThreadID != "" {
			return nil, managedruntime.ErrInvalid
		}
		main, err := s.EnsureAgent(ctx, target)
		if err != nil {
			return nil, err
		}
		action.ThreadID = main.ID
	}
	if action.Kind != "thread_create" && (action.ThreadID == "" || action.ThreadID == work.ThreadID) {
		return nil, managedruntime.ErrInvalid
	}
	if action.Kind == "thread_create" || action.Kind == "thread_send" || action.Kind == "agent_send" {
		if strings.TrimSpace(action.Query) == "" || len(action.Query) > 256<<10 {
			return nil, managedruntime.ErrInvalid
		}
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err = lockAgentAdmission(ctx, tx, work.Scope, target); err != nil {
		return nil, err
	}
	if err = checkScope(ctx, tx, target); err != nil {
		return nil, err
	}
	if action.Kind == "thread_create" || action.Kind == "thread_archive" || action.Kind == "thread_restore" {
		if err := threadGraph(ctx, tx, work.Scope.AgentID); err != nil {
			return nil, err
		}
	}
	ids := []string{work.ThreadID}
	if action.Kind != "thread_create" {
		ids = append(ids, action.ThreadID)
	}
	rows, err := tx.Query(ctx, `SELECT `+threadColumns+` FROM runtime.threads t WHERE t.id=ANY($1::uuid[]) AND t.agent_id=ANY($2::uuid[]) ORDER BY t.id FOR UPDATE OF t`, ids, []string{work.Scope.AgentID, target.AgentID})
	if err != nil {
		return nil, classify(err)
	}
	threads := map[string]managedruntime.Thread{}
	for rows.Next() {
		thread, scanErr := scanThread(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		threads[thread.ID] = thread
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if _, exists := threads[work.ThreadID]; !exists {
		return nil, managedruntime.ErrDenied
	}
	if err = subscriptionAction(ctx, tx, work); err != nil {
		return nil, err
	}
	var previous json.RawMessage
	var matches bool
	err = tx.QueryRow(ctx, `SELECT result,request=$2::jsonb AND target_agent_id=$3 FROM runtime.thread_actions WHERE action_id=$1`, work.ID, encoded, target.AgentID).Scan(&previous, &matches)
	if err == nil {
		if !matches {
			return nil, managedruntime.ErrConflict
		}
		return previous, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if len(threads) != len(ids) {
		return nil, managedruntime.ErrDenied
	}
	thread := threads[action.ThreadID]
	if action.Kind != "thread_create" && (thread.AgentID != target.AgentID || thread.Retention != "active" && action.Kind != "thread_archive" && action.Kind != "thread_restore") {
		return nil, managedruntime.ErrDenied
	}
	var result any
	switch action.Kind {
	case "thread_create":
		var config managedruntime.TurnConfig
		var encodedConfig []byte
		if err = tx.QueryRow(ctx, `SELECT config FROM runtime.turns WHERE id=$1`, work.TurnID).Scan(&encodedConfig); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(encodedConfig, &config); err != nil {
			return nil, err
		}
		thread, err = createWorker(ctx, tx, work.Scope, threads[work.ThreadID], work.ID, action.Name, config.WorkerDepth)
		if err == nil && action.Subscribe {
			err = subscribeThread(ctx, tx, work.Scope, work.ThreadID, thread.ID)
		}
		if err == nil {
			var receipt managedruntime.InputReceipt
			receipt, err = acceptThreadInput(ctx, tx, work.Scope, thread, managedruntime.InputRequest{RequestID: work.ID, Text: action.Query}, threadSource(work, "worker_message"))
			result = map[string]any{"thread": thread, "receipt": receipt, "subscribed": action.Subscribe}
		}
	case "thread_send", "agent_send":
		kind := "worker_message"
		if action.Kind == "agent_send" {
			kind = "peer_message"
		}
		result, err = acceptThreadInput(ctx, tx, target, thread, managedruntime.InputRequest{RequestID: "message:" + work.ID, Text: action.Query}, threadSource(work, kind))
	case "thread_subscribe":
		if thread.Kind != "worker" {
			return nil, managedruntime.ErrInvalid
		}
		err = subscribeThread(ctx, tx, work.Scope, work.ThreadID, thread.ID)
		result = map[string]any{"thread_id": thread.ID, "subscribed": true}
	case "thread_unsubscribe":
		_, err = tx.Exec(ctx, `UPDATE runtime.thread_subscriptions SET enabled=false,generation=generation+CASE WHEN enabled THEN 1 ELSE 0 END WHERE thread_id=$1 AND worker_id=$2`, work.ThreadID, thread.ID)
		result = map[string]any{"thread_id": thread.ID, "subscribed": false}
	case "thread_archive", "thread_restore":
		result, err = setThreadArchived(ctx, tx, work.Scope, thread, action.Kind == "thread_archive")
	case "thread_stop":
		if thread.Kind != "worker" {
			return nil, managedruntime.ErrInvalid
		}
		err = cancelThread(ctx, tx, work.Scope, thread)
		result = map[string]string{"thread_id": thread.ID, "state": "cancel_requested"}
	default:
		return nil, managedruntime.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	value, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO runtime.thread_actions(action_id,request,target_agent_id,result) VALUES($1,$2,$3,$4)`, work.ID, encoded, target.AgentID, value); err != nil {
		return nil, err
	}
	return value, tx.Commit(ctx)
}

func threadSource(work managedruntime.ToolWork, kind string) managedruntime.InputSource {
	return managedruntime.InputSource{Kind: kind, SenderAgentID: work.Scope.AgentID, SenderThreadID: work.ThreadID, SenderTurnID: work.TurnID}
}

func subscribeThread(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, subscriber, worker string) error {
	encoded, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime.thread_subscriptions(agent_id,thread_id,worker_id,scope) VALUES($1,$2,$3,$4) ON CONFLICT(thread_id,worker_id) DO UPDATE SET scope=EXCLUDED.scope,enabled=true,generation=thread_subscriptions.generation+1`, scope.AgentID, subscriber, worker, encoded)
	return err
}

// The producer Thread lock orders completion against subscription changes.
// Only final text is published; thinking and tool history remain private.
func threadResult(ctx context.Context, tx pgx.Tx, thread, turn, state, text string) error {
	truncated := len(text) > 16384
	if truncated {
		text = text[:16384]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	data, err := json.Marshal(map[string]any{"thread_id": thread, "turn_id": turn, "state": state, "text": text, "truncated": truncated})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime.thread_deliveries(subscription_id,generation,turn_id,text) SELECT s.id,s.generation,$2,$3 FROM runtime.thread_subscriptions s JOIN runtime.threads t ON t.id=s.worker_id WHERE s.worker_id=$1 AND s.enabled AND t.kind='worker' ON CONFLICT DO NOTHING`, thread, turn, "Worker result (explicit collaboration data):\n"+string(data))
	return err
}

func (s *Store) ClaimThreadDelivery(ctx context.Context, holder string) (managedruntime.ThreadDelivery, error) {
	var delivery managedruntime.ThreadDelivery
	if holder == "" {
		return delivery, managedruntime.ErrInvalid
	}
	var scope []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM runtime.thread_deliveries WHERE state='pending' AND lease_until<=clock_timestamp() ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE runtime.thread_deliveries d SET lease_epoch=d.lease_epoch+1,lease_until=clock_timestamp()+interval '30 seconds' FROM candidate c,runtime.thread_subscriptions s WHERE d.id=c.id AND s.id=d.subscription_id
 RETURNING d.id,s.thread_id,s.id,s.scope,d.generation,d.lease_epoch,d.text,s.agent_id,s.worker_id,d.turn_id`).Scan(&delivery.ID, &delivery.ThreadID, &delivery.SubscriptionID, &scope, &delivery.Generation, &delivery.LeaseEpoch, &delivery.Text, &delivery.Source.SenderAgentID, &delivery.Source.SenderThreadID, &delivery.Source.SenderTurnID)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery, managedruntime.ErrNoWork
	}
	if err != nil {
		return delivery, err
	}
	delivery.Source.Kind = "thread_result"
	delivery.Source.SubscriptionID = delivery.SubscriptionID
	delivery.Source.Generation = delivery.Generation
	err = json.Unmarshal(scope, &delivery.Scope)
	return delivery, err
}

func (s *Store) FinishThreadDelivery(ctx context.Context, delivery managedruntime.ThreadDelivery, valid bool) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockAgentAdmission(ctx, tx, delivery.Scope); err != nil {
		return err
	}
	thread, err := readThread(ctx, tx, delivery.Scope.AgentID, delivery.ThreadID)
	if err != nil {
		return err
	}
	var enabled bool
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT enabled,generation FROM runtime.thread_subscriptions WHERE id=$1 FOR UPDATE`, delivery.SubscriptionID).Scan(&enabled, &generation); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.thread_deliveries SET state='skipped',lease_until='-infinity' WHERE id=$1 AND state='pending' AND lease_epoch=$2 AND lease_until>clock_timestamp()`, delivery.ID, delivery.LeaseEpoch)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if !enabled || generation != delivery.Generation || thread.Retention != "active" {
		return tx.Commit(ctx)
	}
	if !valid || thread.Application != "" {
		if _, err = tx.Exec(ctx, `UPDATE runtime.thread_subscriptions SET enabled=false,generation=generation+1 WHERE id=$1`, delivery.SubscriptionID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	request := managedruntime.InputRequest{RequestID: fmt.Sprintf("thread_result:%s:%d:%s", delivery.SubscriptionID, delivery.Generation, delivery.Source.SenderTurnID), Text: delivery.Text}
	if _, err = acceptThreadInput(ctx, tx, delivery.Scope, thread, request, delivery.Source); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime.thread_deliveries SET state='delivered' WHERE id=$1`, delivery.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
