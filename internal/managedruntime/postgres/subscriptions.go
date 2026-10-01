package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// Subscription mutations and delivery use the same Thread lock as cancellation.
func subscriptionAction(ctx context.Context, tx pgx.Tx, work managedruntime.ToolWork) error {
	thread, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID)
	if err != nil {
		return err
	}
	if thread.Retention != "active" {
		return managedruntime.ErrDenied
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE j.id=$1 AND j.lease_epoch=$2 AND j.lease_until>clock_timestamp() AND t.thread_id=$3 AND t.state='waiting' AND NOT j.cancel_requested AND NOT j.consumed)`, work.ID, work.LeaseEpoch, work.ThreadID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return managedruntime.ErrFence
	}
	return nil
}

const subscriptionColumns = `id,thread_id,kind,environment_id,operation_id,method,generation,enabled`

func observationInputActive(ctx context.Context, tx pgx.Tx, input string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(i.source->>'kind','')<>'observation' OR EXISTS(SELECT 1 FROM runtime.subscriptions s WHERE s.id::text=i.source->>'subscription_id' AND s.enabled AND s.generation::text=i.source->>'generation') FROM runtime.inputs i WHERE i.id=$1`, input).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return managedruntime.ErrDenied
	}
	return nil
}

func scanSubscription(row pgx.Row) (managedruntime.Subscription, error) {
	var sub managedruntime.Subscription
	err := row.Scan(&sub.ID, &sub.ThreadID, &sub.Kind, &sub.EnvironmentID, &sub.OperationID, &sub.Method, &sub.Generation, &sub.Enabled)
	return sub, classify(err)
}

func (s *Store) ApplySubscription(ctx context.Context, work managedruntime.ToolWork, request managedruntime.SubscriptionRequest, version, offset int64, capability execprotocol.Capability, baseline json.RawMessage) (managedruntime.Subscription, error) {
	var sub managedruntime.Subscription
	if request.EnvironmentID == "" || len(request.Method) > 256 || offset < 0 || !json.Valid(baseline) {
		return sub, managedruntime.ErrInvalid
	}
	switch request.Kind {
	case "environment.presence":
		if request.OperationID != "" || request.Method != "" || capability != "" {
			return sub, managedruntime.ErrInvalid
		}
	case "mcp.notification":
		if request.OperationID == "" || capability != execprotocol.MCP {
			return sub, managedruntime.ErrInvalid
		}
	case "command.observation":
		if request.OperationID == "" || request.Method != "" || capability != execprotocol.Shell {
			return sub, managedruntime.ErrInvalid
		}
	case "operation.terminal":
		if request.OperationID == "" || request.Method != "" || (capability != execprotocol.MCP && capability != execprotocol.Shell) {
			return sub, managedruntime.ErrInvalid
		}
	default:
		return sub, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return sub, err
	}
	defer rollback(tx)
	if err = subscriptionAction(ctx, tx, work); err != nil {
		return sub, err
	}
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT result FROM runtime.subscription_actions WHERE action_id=$1 AND kind='subscribe'`, work.ID).Scan(&previous)
	if err == nil {
		if err = json.Unmarshal(previous, &sub); err != nil {
			return sub, err
		}
		return sub, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sub, err
	}
	if request.OperationID != "" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM runtime.observation_sources WHERE environment_id=$1 AND operation_id=$2 AND agent_id=$3 FOR UPDATE`, request.EnvironmentID, request.OperationID, work.Scope.AgentID).Scan(&id); err != nil {
			return sub, classify(err)
		}
	}
	encoded, err := json.Marshal(work.Scope)
	if err != nil {
		return sub, err
	}
	sub, err = scanSubscription(tx.QueryRow(ctx, `INSERT INTO runtime.subscriptions(action_id,thread_id,agent_id,scope,kind,environment_id,operation_id,method,authorization_version,start_offset,capability,baseline)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
 ON CONFLICT(thread_id,kind,environment_id,operation_id,method) DO UPDATE SET
 action_id=EXCLUDED.action_id,scope=EXCLUDED.scope,enabled=true,
 generation=subscriptions.generation+1,
 authorization_version=EXCLUDED.authorization_version,capability=EXCLUDED.capability,
 start_offset=EXCLUDED.start_offset,baseline=EXCLUDED.baseline
 RETURNING `+subscriptionColumns, work.ID, work.ThreadID, work.Scope.AgentID, encoded, request.Kind, request.EnvironmentID, request.OperationID, request.Method, version, offset, capability, baseline))
	if err != nil {
		return sub, err
	}
	if request.Kind == "environment.presence" {
		// An initial reconciliation closes the gap between reading presence and
		// committing the subscription. Delivery compares fresh current state.
		if _, err = tx.Exec(ctx, `INSERT INTO runtime.observations(event_id,agent_id,kind,data,environment_id,created_at,consumed_at) VALUES($1,$2,'environment.presence',$3,$4,clock_timestamp(),clock_timestamp()) ON CONFLICT DO NOTHING`, work.ID, work.Scope.AgentID, baseline, request.EnvironmentID); err != nil {
			return sub, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO runtime.observation_deliveries(subscription_id,generation,observation_id,agent_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, sub.ID, sub.Generation, work.ID, work.Scope.AgentID); err != nil {
			return sub, err
		}
	} else {
		// The source lock orders registration with observer commits. Facts
		// captured since the sampled offset cannot fall between both sides.
		if _, err = tx.Exec(ctx, `INSERT INTO runtime.observation_deliveries(subscription_id,generation,observation_id,agent_id)
 SELECT $1,$2,o.event_id,o.agent_id FROM runtime.observations o WHERE o.agent_id=$3 AND o.environment_id=$4 AND o.operation_id=$5 AND (($6='operation.terminal' AND o.kind='operation.terminal') OR ($6='mcp.notification' AND o.source_offset>$7 AND o.kind IN ('mcp.notification','mcp.invalid_notification') AND ($8='' OR o.data->>'method'=$8 OR o.kind='mcp.invalid_notification')) OR ($6='command.observation' AND o.source_offset>$7 AND o.kind IN ('command.observation','command.invalid_observation','command.exit','operation.output_expired'))) ON CONFLICT DO NOTHING`, sub.ID, sub.Generation, work.Scope.AgentID, request.EnvironmentID, request.OperationID, request.Kind, offset, request.Method); err != nil {
			return sub, err
		}
	}
	if err = cancelSubscriptionInputs(ctx, tx, work.ThreadID, sub.ID, sub.Generation); err != nil {
		return sub, err
	}
	result, err := json.Marshal(sub)
	if err != nil {
		return sub, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO runtime.subscription_actions(action_id,subscription_id,kind,result) VALUES($1,$2,'subscribe',$3)`, work.ID, sub.ID, result); err != nil {
		return sub, err
	}
	return sub, tx.Commit(ctx)
}

func (s *Store) Unsubscribe(ctx context.Context, work managedruntime.ToolWork, id string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = subscriptionAction(ctx, tx, work); err != nil {
		return err
	}
	var applied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.subscription_actions WHERE action_id=$1 AND kind='unsubscribe')`, work.ID).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return tx.Commit(ctx)
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.subscriptions SET enabled=false,generation=generation+CASE WHEN enabled THEN 1 ELSE 0 END WHERE id=$1 AND thread_id=$2`, id, work.ThreadID)
	if err != nil {
		return classify(err)
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrDenied
	}
	if err = cancelSubscriptionInputs(ctx, tx, work.ThreadID, id, 0); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO runtime.subscription_actions(action_id,subscription_id,kind,result) VALUES($1,$2,'unsubscribe','{}')`, work.ID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Subscriptions(ctx context.Context, scope managedruntime.Scope, thread string) ([]managedruntime.Subscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT s.id,s.thread_id,s.kind,s.environment_id,s.operation_id,s.method,s.generation,s.enabled FROM runtime.subscriptions s JOIN runtime.agents a ON a.id=s.agent_id WHERE s.thread_id=$1 AND a.id=$2 AND a.tenant_id=$3 AND a.user_id=$4 AND a.fleet_id=$5 ORDER BY s.id`, thread, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []managedruntime.Subscription{}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

func (s *Store) ClaimObservationDelivery(ctx context.Context, holder string) (managedruntime.ObservationDelivery, error) {
	var delivery managedruntime.ObservationDelivery
	if holder == "" {
		return delivery, managedruntime.ErrInvalid
	}
	var scope []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM runtime.observation_deliveries WHERE state='pending' AND lease_until<=clock_timestamp() ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE runtime.observation_deliveries d SET lease_epoch=d.lease_epoch+1,lease_holder=$1,lease_until=clock_timestamp()+interval '30 seconds'
 FROM candidate c,runtime.subscriptions s,runtime.observations o WHERE d.id=c.id AND s.id=d.subscription_id AND o.event_id=d.observation_id AND o.agent_id=d.agent_id
 RETURNING d.id,s.thread_id,s.id,s.scope,d.generation,d.lease_epoch,s.authorization_version,s.kind,s.capability,s.baseline,o.event_id,o.kind,o.environment_id,o.operation_id,o.source_offset,o.data,o.created_at`, holder).Scan(&delivery.ID, &delivery.ThreadID, &delivery.SubscriptionID, &scope, &delivery.Generation, &delivery.LeaseEpoch, &delivery.AuthorizationVersion, &delivery.Kind, &delivery.Capability, &delivery.Baseline, &delivery.Observation.ID, &delivery.Observation.Kind, &delivery.Observation.EnvironmentID, &delivery.Observation.OperationID, &delivery.Observation.Offset, &delivery.Observation.Data, &delivery.Observation.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery, managedruntime.ErrNoWork
	}
	if err != nil {
		return delivery, err
	}
	err = json.Unmarshal(scope, &delivery.Scope)
	return delivery, err
}

func (s *Store) FinishObservationDelivery(ctx context.Context, delivery managedruntime.ObservationDelivery, valid bool, baseline json.RawMessage) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	thread, err := readThread(ctx, tx, delivery.Scope.AgentID, delivery.ThreadID)
	if err != nil {
		return err
	}
	var enabled, changed bool
	var generation int64
	// Compare current presence, not outbox timestamps: late offline events must
	// not overwrite a newer online baseline or cause duplicate model calls.
	if baseline == nil {
		baseline = json.RawMessage(`{}`)
	}
	err = tx.QueryRow(ctx, `SELECT enabled,generation,baseline<>$2::jsonb FROM runtime.subscriptions WHERE id=$1 FOR UPDATE`, delivery.SubscriptionID, baseline).Scan(&enabled, &generation, &changed)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.observation_deliveries SET state='skipped',lease_until='-infinity',lease_holder='' WHERE id=$1 AND state='pending' AND lease_epoch=$2 AND lease_until>clock_timestamp()`, delivery.ID, delivery.LeaseEpoch)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if !enabled || generation != delivery.Generation || thread.Retention != "active" {
		return tx.Commit(ctx)
	}
	if !valid {
		if _, err = tx.Exec(ctx, `UPDATE runtime.subscriptions SET enabled=false,generation=generation+1 WHERE id=$1`, delivery.SubscriptionID); err != nil {
			return err
		}
		if err = cancelSubscriptionInputs(ctx, tx, thread.ID, delivery.SubscriptionID, 0); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if delivery.Kind == "environment.presence" {
		if !changed {
			return tx.Commit(ctx)
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime.subscriptions SET baseline=$2 WHERE id=$1`, delivery.SubscriptionID, baseline); err != nil {
			return err
		}
		delivery.Observation.Data = baseline
	}
	source := managedruntime.InputSource{Kind: "observation", ObservationID: delivery.Observation.ID, SubscriptionID: delivery.SubscriptionID, Generation: delivery.Generation, EnvironmentID: delivery.Observation.EnvironmentID, AuthorizationVersion: delivery.AuthorizationVersion, Capability: delivery.Capability}
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	text := managedruntime.ObservationNotice(delivery.Observation)
	scope := delivery.Scope
	requestID := fmt.Sprintf("observation:%s:%d:%s", delivery.SubscriptionID, delivery.Generation, delivery.Observation.ID)
	var input string
	err = tx.QueryRow(ctx, `INSERT INTO runtime.inputs(request_id,thread_id,actor_id,membership_version,text,membership_execution_epoch,agent_execution_epoch,actor_authorization_epoch,source) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(thread_id,request_id) DO NOTHING RETURNING id`, requestID, thread.ID, scope.ActorID, scope.MembershipVersion, text, scope.MembershipExecutionEpoch, scope.AgentExecutionEpoch, scope.ActorAuthorizationEpoch, encoded).Scan(&input)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if input != "" {
		if _, err = tx.Exec(ctx, `UPDATE runtime.threads SET state='queued' WHERE id=$1 AND state IN ('idle','failed')`, thread.ID); err != nil {
			return err
		}
		if err = appendEvent(ctx, tx, thread.ID, "input.accepted", map[string]any{"id": input, "request_id": requestID, "source": source}); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime.observation_deliveries SET state='delivered' WHERE id=$1`, delivery.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cancelSubscriptionInputs(ctx context.Context, tx pgx.Tx, thread, id string, keepGeneration int64) error {
	if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='cancelled' WHERE thread_id=$1 AND state='queued' AND source->>'subscription_id'=$2 AND source->>'generation'<>$3`, thread, id, fmt.Sprint(keepGeneration)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='idle' WHERE id=$1 AND state='queued' AND NOT EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state IN ('queued','active'))`, thread)
	return err
}
