package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) ObservationSources(ctx context.Context, scope managedruntime.Scope, after string) (managedruntime.ObservationPage, error) {
	page := managedruntime.ObservationPage{Sources: []managedruntime.ObservationStatus{}, Targets: []managedruntime.ObserverTarget{}, Controls: []managedruntime.ObserverControl{}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `SELECT o.id,o.thread_id,CASE WHEN o.control_id IS NULL THEN 'model' ELSE 'manual' END,COALESCE(o.control_id::text,''),o.environment_id,o.operation_id,o.kind,o.working_directory,o.cursor,o.closed,o.stop_requested FROM runtime.observation_sources o JOIN runtime.agents a ON a.id=o.agent_id
 WHERE a.id=$1 AND a.tenant_id=$2 AND a.user_id=$3 AND a.fleet_id=$4 AND ($5='' OR (o.created_at,o.id)<(SELECT created_at,id FROM runtime.observation_sources WHERE id=NULLIF($5,'')::uuid AND agent_id=$1)) ORDER BY o.created_at DESC,o.id DESC LIMIT 51`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID, after)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var v managedruntime.ObservationStatus
		v.Subscriptions = []managedruntime.Subscription{}
		if err = rows.Scan(&v.ID, &v.ThreadID, &v.Origin, &v.ControlID, &v.EnvironmentID, &v.OperationID, &v.Kind, &v.Directory, &v.Cursor, &v.Closed, &v.StopRequested); err != nil {
			rows.Close()
			return page, err
		}
		page.Sources = append(page.Sources, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Sources) > 50 {
		page.Next = page.Sources[49].ID
		page.Sources = page.Sources[:50]
	}
	controls := map[string]bool{}
	for index := range page.Sources {
		source := &page.Sources[index]
		rows, err = tx.Query(ctx, `SELECT `+subscriptionColumns+` FROM runtime.subscriptions WHERE agent_id=$1 AND environment_id=$2 AND operation_id=$3 ORDER BY id LIMIT 101`, scope.AgentID, source.EnvironmentID, source.OperationID)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			sub, e := scanSubscription(rows)
			if e != nil {
				rows.Close()
				return page, e
			}
			source.Subscriptions = append(source.Subscriptions, sub)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return page, err
		}
		if len(source.Subscriptions) > 100 {
			source.SubscriptionsTruncated = true
			source.Subscriptions = source.Subscriptions[:100]
		}
		if source.ControlID != "" && !controls[source.ControlID] {
			control, e := scanObserverControl(tx.QueryRow(ctx, `SELECT `+observerControlColumns+` FROM runtime.observer_controls WHERE id=$1`, source.ControlID))
			if e != nil {
				return page, e
			}
			page.Controls = append(page.Controls, control)
			controls[source.ControlID] = true
		}
	}
	rows, err = tx.Query(ctx, `SELECT t.id,t.name FROM runtime.threads t JOIN runtime.agents a ON a.id=t.agent_id WHERE a.id=$1 AND a.tenant_id=$2 AND a.user_id=$3 AND a.fleet_id=$4 AND t.retention='active' AND t.application='' ORDER BY t.created_at,t.id LIMIT 101`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var target managedruntime.ObserverTarget
		if err = rows.Scan(&target.ID, &target.Name); err != nil {
			rows.Close()
			return page, err
		}
		page.Targets = append(page.Targets, target)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Targets) > 100 {
		page.TargetsTruncated = true
		page.Targets = page.Targets[:100]
	}
	return page, tx.Commit(ctx)
}
func (s *Store) ObserverSource(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.ObservationSource, error) {
	var source managedruntime.ObservationSource
	var encoded []byte
	err := s.pool.QueryRow(ctx, `SELECT o.id,o.thread_id,o.environment_id,o.operation_id,o.kind,o.scope,o.authorization_version FROM runtime.observation_sources o JOIN runtime.agents a ON a.id=o.agent_id WHERE o.id=$1 AND a.id=$2 AND a.tenant_id=$3 AND a.user_id=$4 AND a.fleet_id=$5`, id, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&source.ID, &source.ThreadID, &source.EnvironmentID, &source.OperationID, &source.Kind, &encoded, &source.AuthorizationVersion)
	if err != nil {
		return source, classify(err)
	}
	err = json.Unmarshal(encoded, &source.Scope)
	return source, err
}
func (s *Store) ObservedEvents(ctx context.Context, scope managedruntime.Scope, sourceID, after string) (managedruntime.ObservedEvents, error) {
	result := managedruntime.ObservedEvents{Items: []managedruntime.ObservedEvent{}}
	source, err := s.ObserverSource(ctx, scope, sourceID)
	if err != nil {
		return result, err
	}
	rows, err := s.pool.Query(ctx, `SELECT o.event_id,o.kind,o.environment_id,o.operation_id,o.source_offset,
 CASE WHEN octet_length(o.data::text)>16384 THEN jsonb_build_object('preview',left(o.data::text,4096)) ELSE o.data END,o.created_at,octet_length(o.data::text)>16384,
 (SELECT count(*) FROM runtime.observation_deliveries d WHERE d.observation_id=o.event_id AND d.agent_id=o.agent_id AND d.state='pending'),
 (SELECT count(*) FROM runtime.observation_deliveries d WHERE d.observation_id=o.event_id AND d.agent_id=o.agent_id AND d.state='delivered'),
 (SELECT count(*) FROM runtime.observation_deliveries d WHERE d.observation_id=o.event_id AND d.agent_id=o.agent_id AND d.state='skipped')
 FROM runtime.observations o WHERE o.agent_id=$1 AND o.environment_id=$2 AND o.operation_id=$3 AND ($4='' OR (o.created_at,o.event_id)<(SELECT created_at,event_id FROM runtime.observations WHERE event_id=NULLIF($4,'')::uuid AND agent_id=$1 AND environment_id=$2 AND operation_id=$3)) ORDER BY o.created_at DESC,o.event_id DESC LIMIT 21`, scope.AgentID, source.EnvironmentID, source.OperationID, after)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var v managedruntime.ObservedEvent
		if err = rows.Scan(&v.ID, &v.Kind, &v.EnvironmentID, &v.OperationID, &v.Offset, &v.Data, &v.CreatedAt, &v.DataTruncated, &v.Pending, &v.Delivered, &v.Skipped); err != nil {
			return result, err
		}
		result.Items = append(result.Items, v)
	}
	if len(result.Items) > 20 {
		result.Next = result.Items[19].ID
		result.Items = result.Items[:20]
	}
	return result, rows.Err()
}

func (s *Store) CurrentObserverSource(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.ObservationSource, error) {
	var current string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(m.source_id,o.id) FROM runtime.observation_sources o JOIN runtime.agents a ON a.id=o.agent_id LEFT JOIN runtime.observer_controls m ON m.id=o.control_id WHERE o.id=$1 AND a.id=$2 AND a.tenant_id=$3 AND a.user_id=$4 AND a.fleet_id=$5`, id, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&current)
	if err != nil {
		return managedruntime.ObservationSource{}, classify(err)
	}
	return s.ObserverSource(ctx, scope, current)
}
