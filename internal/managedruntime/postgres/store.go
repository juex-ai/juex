// Package postgres owns the Runtime schema and atomic, fenced transitions.
package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/managedruntime"
)

//go:embed schema.sql
var schema string

//go:embed tools_schema.sql
var toolsSchema string

//go:embed tool_cancellation_schema.sql
var toolCancellationSchema string

//go:embed observations_schema.sql
var observationsSchema string

//go:embed models_schema.sql
var modelsSchema string

//go:embed compaction_schema.sql
var compactionSchema string

//go:embed collaboration_schema.sql
var collaborationSchema string

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.runtime.migrations'));
	CREATE SCHEMA IF NOT EXISTS runtime; CREATE TABLE IF NOT EXISTS runtime.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		return err
	}
	migrations := []string{schema, toolsSchema, toolCancellationSchema, observationsSchema, modelsSchema, compactionSchema, collaborationSchema}
	rows, err := tx.Query(ctx, `SELECT version,checksum FROM runtime.schema_versions ORDER BY version`)
	if err != nil {
		return err
	}
	installed := 0
	for rows.Next() {
		var version int
		var stored string
		if err := rows.Scan(&version, &stored); err != nil {
			rows.Close()
			return err
		}
		if version != installed+1 || version > len(migrations) || stored != fmt.Sprintf("%x", sha256.Sum256([]byte(migrations[version-1]))) {
			rows.Close()
			return fmt.Errorf("unsupported or modified Runtime schema version %d", version)
		}
		installed++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := installed; i < len(migrations); i++ {
		if _, err := tx.Exec(ctx, migrations[i]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256([]byte(migrations[i])))); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) begin(ctx context.Context) (pgx.Tx, error) {
	return s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func classify(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return managedruntime.ErrDenied
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			return managedruntime.ErrConflict
		case "22P02", "23514":
			return managedruntime.ErrInvalid
		case "23503":
			return managedruntime.ErrDenied
		}
	}
	return err
}

func validScope(scope managedruntime.Scope) bool {
	return scope.TenantID != "" && scope.UserID != "" && scope.FleetID != "" && scope.AgentID != "" && scope.ActorID != "" && scope.ActorAuthorizationEpoch > 0 && scope.MembershipVersion > 0 && scope.MembershipExecutionEpoch > 0 && scope.AgentExecutionEpoch > 0
}

// EnsureAgent materializes only immutable ownership and the unique Main Thread.
// It is called with a freshly authorized Management scope, not configuration files.
func (s *Store) EnsureAgent(ctx context.Context, scope managedruntime.Scope) (managedruntime.Thread, error) {
	if !validScope(scope) {
		return managedruntime.Thread{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.Thread{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID); err != nil {
		return managedruntime.Thread{}, classify(err)
	}
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.Thread{}, err
	}
	var created string
	err = tx.QueryRow(ctx, `INSERT INTO runtime.threads(agent_id,kind,name) VALUES($1,'main','Main') ON CONFLICT DO NOTHING RETURNING id`, scope.AgentID).Scan(&created)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return managedruntime.Thread{}, err
	}
	if created != "" {
		if err := appendEvent(ctx, tx, created, "thread.created", map[string]string{"kind": "main"}); err != nil {
			return managedruntime.Thread{}, err
		}
	}
	thread, err := readThread(ctx, tx, scope.AgentID, "")
	if err != nil {
		return thread, err
	}
	return thread, tx.Commit(ctx)
}

func checkScope(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope) error {
	var id string
	return classify(tx.QueryRow(ctx, `SELECT id FROM runtime.agents WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&id))
}

func (s *Store) AcceptInput(ctx context.Context, scope managedruntime.Scope, request managedruntime.InputRequest) (managedruntime.InputReceipt, error) {
	if strings.TrimSpace(request.Text) == "" || len(request.Text) > 256<<10 {
		return managedruntime.InputReceipt{}, managedruntime.ErrInvalid
	}
	return s.acceptInput(ctx, scope, request, managedruntime.InputSource{})
}

func (s *Store) AcceptCompaction(ctx context.Context, scope managedruntime.Scope, thread string, request managedruntime.CompactionRequest) (managedruntime.InputReceipt, error) {
	if len(request.Focus) > 4096 {
		return managedruntime.InputReceipt{}, managedruntime.ErrInvalid
	}
	return s.acceptInput(ctx, scope, managedruntime.InputRequest{RequestID: request.RequestID, ThreadID: thread}, managedruntime.InputSource{Kind: "compaction", Focus: strings.TrimSpace(request.Focus)})
}

func (s *Store) acceptInput(ctx context.Context, scope managedruntime.Scope, request managedruntime.InputRequest, source managedruntime.InputSource) (managedruntime.InputReceipt, error) {
	if !validScope(scope) || request.RequestID == "" || len(request.RequestID) > 200 {
		return managedruntime.InputReceipt{}, managedruntime.ErrInvalid
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.InputReceipt{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.InputReceipt{}, err
	}
	thread, err := readThread(ctx, tx, scope.AgentID, request.ThreadID)
	if err != nil {
		return managedruntime.InputReceipt{}, err
	}
	result, err := acceptThreadInput(ctx, tx, scope, thread, request, source)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func acceptThreadInput(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, thread managedruntime.Thread, request managedruntime.InputRequest, source managedruntime.InputSource) (managedruntime.InputReceipt, error) {
	encodedSource, err := json.Marshal(source)
	if err != nil {
		return managedruntime.InputReceipt{}, err
	}
	if thread.Retention != "active" {
		return managedruntime.InputReceipt{}, managedruntime.ErrDenied
	}
	var result managedruntime.InputReceipt
	var storedText, actor string
	var storedSource []byte
	err = tx.QueryRow(ctx, `INSERT INTO runtime.inputs(request_id,thread_id,actor_id,membership_version,text,membership_execution_epoch,agent_execution_epoch,actor_authorization_epoch,source) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
	ON CONFLICT(thread_id,request_id) DO NOTHING
	RETURNING id,request_id,thread_id,state,accepted_at,text,actor_id,source`, request.RequestID, thread.ID, scope.ActorID, scope.MembershipVersion, request.Text, scope.MembershipExecutionEpoch, scope.AgentExecutionEpoch, scope.ActorAuthorizationEpoch, encodedSource).Scan(&result.ID, &result.RequestID, &result.ThreadID, &result.State, &result.AcceptedAt, &storedText, &actor, &storedSource)
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id,request_id,thread_id,state,accepted_at,text,actor_id,source FROM runtime.inputs WHERE thread_id=$1 AND request_id=$2`, thread.ID, request.RequestID).Scan(&result.ID, &result.RequestID, &result.ThreadID, &result.State, &result.AcceptedAt, &storedText, &actor, &storedSource)
	}
	if err != nil {
		return result, classify(err)
	}
	var origin managedruntime.InputSource
	if err := json.Unmarshal(storedSource, &origin); err != nil {
		return result, err
	}
	if storedText != request.Text || actor != scope.ActorID || origin != source {
		return result, managedruntime.ErrConflict
	}
	if created {
		kind := "input.accepted"
		if source.Kind == "compaction" {
			kind = "context.requested"
		}
		if err := appendEvent(ctx, tx, thread.ID, kind, map[string]any{"receipt": result, "text": request.Text, "source": source}); err != nil {
			return result, err
		}
	}
	// The receipt itself is the durable mailbox fact. Wakeups are hints; polling
	// the queued rows after a restart does not depend on a notification arriving.
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN state IN ('idle','failed') THEN 'queued' ELSE state END,updated_at=clock_timestamp() WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime.inputs WHERE id=$2 AND state='queued')`, thread.ID, result.ID); err != nil {
		return result, err
	}
	return result, nil
}

type scanner interface{ Scan(...any) error }

const threadColumns = `t.id,t.agent_id,COALESCE(t.parent_id::text,''),t.kind,t.name,t.retention,t.state,t.generation,t.sequence,
(SELECT count(*) FROM runtime.inputs i WHERE i.thread_id=t.id AND i.state IN ('queued','active')),t.created_at,t.updated_at`

func scanThread(row scanner) (managedruntime.Thread, error) {
	var v managedruntime.Thread
	err := row.Scan(&v.ID, &v.AgentID, &v.ParentID, &v.Kind, &v.Name, &v.Retention, &v.State, &v.Generation, &v.Sequence, &v.PendingInputs, &v.CreatedAt, &v.UpdatedAt)
	return v, classify(err)
}
func readThread(ctx context.Context, tx pgx.Tx, agentID, threadID string) (managedruntime.Thread, error) {
	if threadID == "" {
		return scanThread(tx.QueryRow(ctx, `SELECT `+threadColumns+` FROM runtime.threads t WHERE t.agent_id=$1 AND t.kind='main' FOR UPDATE`, agentID))
	}
	return scanThread(tx.QueryRow(ctx, `SELECT `+threadColumns+` FROM runtime.threads t WHERE t.agent_id=$1 AND t.id=$2 FOR UPDATE`, agentID, threadID))
}

func appendEvent(ctx context.Context, tx pgx.Tx, threadID, kind string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH next AS(UPDATE runtime.threads SET sequence=sequence+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING sequence,generation)
	INSERT INTO runtime.events(thread_id,sequence,generation,kind,data) SELECT $1,sequence,generation,$2,$3 FROM next`, threadID, kind, encoded)
	return err
}

func (s *Store) Threads(ctx context.Context, scope managedruntime.Scope) ([]managedruntime.Thread, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+threadColumns+` FROM runtime.threads t WHERE agent_id=$1 ORDER BY kind,created_at,id`, scope.AgentID)
	if err != nil {
		return nil, err
	}
	result := []managedruntime.Thread{}
	for rows.Next() {
		v, err := scanThread(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) Timeline(ctx context.Context, scope managedruntime.Scope, threadID string, after int64, limit int) (managedruntime.Timeline, error) {
	if after < 0 || limit < 1 || limit > 500 {
		return managedruntime.Timeline{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.Timeline{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.Timeline{}, err
	}
	thread, err := readThread(ctx, tx, scope.AgentID, threadID)
	if err != nil {
		return managedruntime.Timeline{}, err
	}
	result := managedruntime.Timeline{Thread: thread, Events: []managedruntime.Event{}, NextSequence: after}
	rows, err := tx.Query(ctx, `SELECT id,thread_id,sequence,generation,kind,data,created_at FROM runtime.events WHERE thread_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, thread.ID, after, limit)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var e managedruntime.Event
		if err := rows.Scan(&e.ID, &e.ThreadID, &e.Sequence, &e.Generation, &e.Kind, &e.Data, &e.CreatedAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Events = append(result.Events, e)
		result.NextSequence = e.Sequence
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.HasMore = result.NextSequence < thread.Sequence
	return result, tx.Commit(ctx)
}
