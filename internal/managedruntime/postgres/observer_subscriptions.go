package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) SetSourceSubscription(ctx context.Context, scope managedruntime.Scope, source, threadID string, enabled bool, startOffset int64) (managedruntime.Subscription, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.Subscription{}, err
	}
	defer rollback(tx)
	if err = checkScope(ctx, tx, scope); err != nil {
		return managedruntime.Subscription{}, err
	}
	if err = threadGraph(ctx, tx, scope.AgentID); err != nil {
		return managedruntime.Subscription{}, err
	}
	thread, err := readThread(ctx, tx, scope.AgentID, threadID)
	if err != nil {
		return managedruntime.Subscription{}, err
	}
	if enabled && (thread.Retention != "active" || thread.Application != "") {
		return managedruntime.Subscription{}, managedruntime.ErrDenied
	}
	var control *string
	if err = tx.QueryRow(ctx, `SELECT control_id FROM runtime.observation_sources WHERE id=$1 AND agent_id=$2`, source, scope.AgentID).Scan(&control); err != nil {
		return managedruntime.Subscription{}, classify(err)
	}
	if control != nil {
		var desired, current string
		if err = tx.QueryRow(ctx, `SELECT desired,source_id FROM runtime.observer_controls WHERE id=$1 FOR UPDATE`, *control).Scan(&desired, &current); err != nil {
			return managedruntime.Subscription{}, err
		}
		if current != source {
			return managedruntime.Subscription{}, managedruntime.ErrConflict
		}
		if enabled && desired != "running" {
			return managedruntime.Subscription{}, managedruntime.ErrDenied
		}
	}
	var original managedruntime.Scope
	var owner []byte
	var environment, operation, kind string
	var stopped, closed bool
	var offset, version int64
	err = tx.QueryRow(ctx, `SELECT scope,environment_id,operation_id,kind,stop_requested,closed,cursor,authorization_version FROM runtime.observation_sources WHERE id=$1 AND agent_id=$2 FOR UPDATE`, source, scope.AgentID).Scan(&owner, &environment, &operation, &kind, &stopped, &closed, &offset, &version)
	if err != nil {
		return managedruntime.Subscription{}, classify(err)
	}
	if err = json.Unmarshal(owner, &original); err != nil {
		return managedruntime.Subscription{}, err
	}
	capability := execprotocol.RequiredCapability(kind)
	if enabled && (stopped || closed || !scope.SameAuthority(original) || !scope.Capabilities.Allows(agentpolicy.Observations) || capability == execprotocol.MCP && !scope.Capabilities.Allows(agentpolicy.MCP) || capability == execprotocol.Shell && !scope.Capabilities.Allows(agentpolicy.Shell)) {
		return managedruntime.Subscription{}, managedruntime.ErrDenied
	}
	subscriptionKind := "command.observation"
	switch kind {
	case "mcp_connect":
		subscriptionKind = "mcp.notification"
	case "exec_command":
		subscriptionKind = "operation.terminal"
	}
	if control != nil {
		if err = setObserverSubscriptionIntent(ctx, tx, scope, *control, threadID, subscriptionKind, "", enabled); err != nil {
			return managedruntime.Subscription{}, err
		}
	}
	var sub managedruntime.Subscription
	if enabled {
		encoded, _ := json.Marshal(scope)
		// Setting an already enabled subscription is idempotent, including its cursor.
		sub, err = scanSubscription(tx.QueryRow(ctx, `INSERT INTO runtime.subscriptions(thread_id,agent_id,scope,kind,environment_id,operation_id,authorization_version,capability,start_offset)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(thread_id,kind,environment_id,operation_id,method) DO UPDATE SET enabled=true,generation=subscriptions.generation+CASE WHEN subscriptions.enabled THEN 0 ELSE 1 END,
 scope=EXCLUDED.scope,authorization_version=EXCLUDED.authorization_version,start_offset=CASE WHEN subscriptions.enabled THEN subscriptions.start_offset ELSE EXCLUDED.start_offset END
 RETURNING `+subscriptionColumns, threadID, scope.AgentID, encoded, subscriptionKind, environment, operation, version, capability, startOffset))
	} else {
		encoded, _ := json.Marshal(scope)
		sub, err = scanSubscription(tx.QueryRow(ctx, `INSERT INTO runtime.subscriptions(thread_id,agent_id,scope,kind,environment_id,operation_id,authorization_version,capability,start_offset,enabled)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,0,false)
 ON CONFLICT(thread_id,kind,environment_id,operation_id,method) DO UPDATE SET enabled=false,generation=subscriptions.generation+CASE WHEN subscriptions.enabled THEN 1 ELSE 0 END RETURNING `+subscriptionColumns, threadID, scope.AgentID, encoded, subscriptionKind, environment, operation, version, capability))
	}
	if err != nil {
		return sub, err
	}

	if enabled {
		if err = backfillSourceDelivery(ctx, tx, sub.ID); err != nil {
			return sub, err
		}
	}
	if err = cancelSubscriptionInputs(ctx, tx, threadID, sub.ID, sub.Generation); err != nil {
		return sub, err
	}
	return sub, tx.Commit(ctx)
}
func (s *Store) CleanupStoppedSubscription(ctx context.Context) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var id, thread, agent string
	err = tx.QueryRow(ctx, `SELECT s.id,s.thread_id,s.agent_id FROM runtime.subscriptions s JOIN runtime.observation_sources o ON o.environment_id=s.environment_id AND o.operation_id=s.operation_id AND o.agent_id=s.agent_id WHERE s.enabled AND o.stop_requested ORDER BY s.id LIMIT 1`).Scan(&id, &thread, &agent)
	if errors.Is(err, pgx.ErrNoRows) {
		return managedruntime.ErrNoWork
	}
	if err != nil {
		return err
	}
	if _, err = readThread(ctx, tx, agent, thread); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE runtime.subscriptions SET enabled=false,generation=generation+1 WHERE id=$1 AND enabled`, id); err != nil {
		return err
	}
	if err = cancelSubscriptionInputs(ctx, tx, thread, id, 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Human and model changes share the durable intent, including filtered MCP
// subscriptions. The caller holds graph, Thread and control locks in that order.
func setObserverSubscriptionIntent(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, control, thread, kind, method string, enabled bool) error {
	encoded, _ := json.Marshal(scope)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.observer_subscriptions(control_id,thread_id,scope,kind,method,enabled) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(control_id,thread_id,kind,method) DO UPDATE SET enabled=EXCLUDED.enabled,scope=EXCLUDED.scope`, control, thread, encoded, kind, method, enabled); err != nil {
		return err
	}
	if enabled {
		return nil
	}
	rows, err := tx.Query(ctx, `UPDATE runtime.subscriptions s SET enabled=false,generation=generation+1 WHERE s.thread_id=$1 AND s.kind=$3 AND s.method=$4 AND s.enabled
 AND EXISTS(SELECT 1 FROM runtime.observation_sources o WHERE o.control_id=$2 AND o.agent_id=s.agent_id AND o.environment_id=s.environment_id AND o.operation_id=s.operation_id) RETURNING s.id`, thread, control, kind, method)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = cancelSubscriptionInputs(ctx, tx, thread, id, 0); err != nil {
			return err
		}
	}
	return nil
}

func lockObserverControl(ctx context.Context, tx pgx.Tx, control, source string, enabling bool) error {
	var desired, current string
	if err := tx.QueryRow(ctx, `SELECT desired,source_id FROM runtime.observer_controls WHERE id=$1 FOR UPDATE`, control).Scan(&desired, &current); err != nil {
		return err
	}
	if enabling && current != source {
		return managedruntime.ErrConflict
	}
	if enabling && desired != "running" {
		return managedruntime.ErrDenied
	}
	return nil
}
