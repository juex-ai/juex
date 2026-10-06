package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) ReleaseObservationClaims(ctx context.Context, holder string) error {
	if holder == "" {
		return managedruntime.ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `WITH sources AS (
 UPDATE runtime.observation_sources SET lease_epoch=lease_epoch+1,lease_holder='',lease_until='-infinity',next_check=least(next_check,clock_timestamp()) WHERE lease_holder=$1)
 UPDATE runtime.observation_deliveries SET lease_epoch=lease_epoch+1,lease_holder='',lease_until='-infinity' WHERE lease_holder=$1`, holder)
	return err
}

func (s *Store) ClaimObservation(ctx context.Context, holder string) (managedruntime.ObservationSource, error) {
	var source managedruntime.ObservationSource
	if holder == "" {
		return source, managedruntime.ErrInvalid
	}
	var scope, options, command []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM runtime.observation_sources WHERE NOT closed AND lease_until<=clock_timestamp() AND next_check<=clock_timestamp() ORDER BY next_check,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE runtime.observation_sources o SET lease_epoch=o.lease_epoch+1,lease_holder=$1,lease_until=clock_timestamp()+interval '30 seconds' FROM candidate c,runtime.tools j WHERE o.id=c.id AND j.id=o.id
 RETURNING o.id,o.thread_id,o.environment_id,o.operation_id,o.kind,o.scope,o.cursor,o.pending,o.discarding,o.lease_epoch,o.wake_version,j.state IN ('pending','waiting'),o.options,o.command_batch,o.working_directory,o.authorization_version`, holder).Scan(&source.ID, &source.ThreadID, &source.EnvironmentID, &source.OperationID, &source.Kind, &scope, &source.Cursor, &source.Pending, &source.Discarding, &source.LeaseEpoch, &source.WakeVersion, &source.DeliveryPending, &options, &command, &source.WorkingDirectory, &source.AuthorizationVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, managedruntime.ErrNoWork
	}
	if err != nil {
		return source, err
	}
	err = errors.Join(json.Unmarshal(scope, &source.Scope), json.Unmarshal(options, &source.Options), json.Unmarshal(command, &source.Command))
	return source, err
}

func (s *Store) FinishObservation(ctx context.Context, source managedruntime.ObservationSource, batch managedruntime.ObservationBatch) error {
	if batch.Cursor < source.Cursor || len(batch.Pending) > 1<<20 || len(batch.Facts) > 4096 {
		return managedruntime.ErrInvalid
	}
	command, err := json.Marshal(batch.Command)
	if err != nil || len(command) > 1<<20 {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	result, err := tx.Exec(ctx, `UPDATE runtime.observation_sources SET cursor=$3,pending=$4,discarding=$5,closed=$6,command_batch=$10,lease_until='-infinity',lease_holder='',
 next_check=CASE WHEN wake_version<>$7 OR $8 THEN clock_timestamp() WHEN $9::double precision>0 THEN clock_timestamp()+make_interval(secs=>$9) ELSE 'infinity'::timestamptz END
 WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp()`, source.ID, source.LeaseEpoch, batch.Cursor, nonnullBytes(batch.Pending), batch.Discarding, batch.Closed, source.WakeVersion, batch.More, batch.RetryAfter.Seconds(), command)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	for _, fact := range batch.Facts {
		if fact.EnvironmentID != source.EnvironmentID || fact.OperationID != source.OperationID || !json.Valid(fact.Data) {
			return managedruntime.ErrInvalid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.observations(event_id,agent_id,kind,data,created_at,environment_id,operation_id,source_offset) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, fact.ID, source.Scope.AgentID, fact.Kind, fact.Data, fact.CreatedAt, fact.EnvironmentID, fact.OperationID, fact.Offset); err != nil {
			return err
		}
		if err := enqueueObservationDeliveries(ctx, tx, fact.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nonnullBytes(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func enqueueObservationDeliveries(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO runtime.observation_deliveries(subscription_id,generation,observation_id,agent_id)
 SELECT s.id,s.generation,o.event_id,o.agent_id FROM runtime.observations o JOIN runtime.subscriptions s ON s.agent_id=o.agent_id AND s.environment_id=o.environment_id AND s.enabled
 WHERE o.event_id=$1 AND ((s.kind='environment.presence' AND o.operation_id='' AND o.kind LIKE 'environment.%') OR (s.operation_id=o.operation_id AND s.operation_id<>'' AND ((s.kind='mcp.notification' AND o.kind IN ('mcp.notification','mcp.invalid_notification') AND o.source_offset>s.start_offset AND (s.method='' OR o.data->>'method'=s.method OR o.kind='mcp.invalid_notification')) OR (s.kind='operation.terminal' AND o.kind='operation.terminal') OR (s.kind='command.observation' AND o.kind IN ('command.observation','command.invalid_observation','command.exit','operation.output_expired') AND o.source_offset>s.start_offset))))
 ON CONFLICT DO NOTHING`, id)
	return err
}

func (s *Store) ObservationAcks(ctx context.Context, limit int) ([]managedruntime.ObservationAck, error) {
	if limit < 1 || limit > 100 {
		return nil, managedruntime.ErrInvalid
	}
	// Acknowledgments race with pagination on the same source row. Use Runtime's
	// lock-based isolation rather than inheriting the connection default.
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `WITH candidate AS (SELECT id FROM runtime.observation_sources WHERE kind IN ('mcp_connect','observe_command') AND confirmed_cursor<cursor AND ack_next_check<=clock_timestamp() ORDER BY ack_next_check,id FOR UPDATE SKIP LOCKED LIMIT $1)
 UPDATE runtime.observation_sources o SET ack_next_check=clock_timestamp()+interval '5 seconds' FROM candidate c WHERE o.id=c.id RETURNING o.id,o.environment_id,o.operation_id,o.scope,o.cursor`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var acknowledgments []managedruntime.ObservationAck
	for rows.Next() {
		var ack managedruntime.ObservationAck
		var scope []byte
		if err := rows.Scan(&ack.SourceID, &ack.EnvironmentID, &ack.OperationID, &scope, &ack.Cursor); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scope, &ack.Scope); err != nil {
			return nil, err
		}
		acknowledgments = append(acknowledgments, ack)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return acknowledgments, tx.Commit(ctx)
}

func (s *Store) ConfirmObservationAck(ctx context.Context, ack managedruntime.ObservationAck) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `UPDATE runtime.observation_sources SET confirmed_cursor=greatest(confirmed_cursor,$2),ack_next_check='-infinity' WHERE id=$1 AND cursor>=$2`, ack.SourceID, ack.Cursor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Observation(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.Observation, error) {
	var fact managedruntime.Observation
	err := s.pool.QueryRow(ctx, `SELECT o.event_id,o.kind,o.environment_id,o.operation_id,o.source_offset,o.data,o.created_at FROM runtime.observations o JOIN runtime.agents a ON a.id=o.agent_id WHERE o.event_id=$1 AND a.id=$2 AND a.tenant_id=$3 AND a.user_id=$4 AND a.fleet_id=$5`, id, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&fact.ID, &fact.Kind, &fact.EnvironmentID, &fact.OperationID, &fact.Offset, &fact.Data, &fact.CreatedAt)
	return fact, classify(err)
}
