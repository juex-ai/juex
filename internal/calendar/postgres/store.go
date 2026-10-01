// Package postgres owns the independent Calendar schema.
package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
)

//go:embed schema.sql
var schema string

//go:embed purge_schema.sql
var purgeSchema string

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.calendar.migrations')); CREATE SCHEMA IF NOT EXISTS calendar; CREATE TABLE IF NOT EXISTS calendar.schema_versions(version integer PRIMARY KEY, checksum text NOT NULL)`); err != nil {
		return err
	}
	migrations := []string{schema, purgeSchema}
	for i, migration := range migrations {
		checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(migration)))
		var stored string
		err = tx.QueryRow(ctx, `SELECT checksum FROM calendar.schema_versions WHERE version=$1`, i+1).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err = tx.Exec(ctx, migration); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO calendar.schema_versions VALUES($1,$2)`, i+1, checksum); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if stored != checksum {
			return errors.New("modified calendar schema")
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM calendar.schema_versions`).Scan(&count); err != nil {
		return err
	}
	if count != len(migrations) {
		return errors.New("unsupported calendar schema")
	}
	return tx.Commit(ctx)
}

func (s *Store) View(ctx context.Context, scope application.Scope, fn func(*calendar.State) error) error {
	return s.transaction(ctx, scope, false, fn)
}
func (s *Store) Update(ctx context.Context, scope application.Scope, fn func(*calendar.State) error) error {
	return s.transaction(ctx, scope, true, fn)
}

func (s *Store) transaction(ctx context.Context, scope application.Scope, write bool, fn func(*calendar.State) error) error {
	if !scope.Valid() {
		return application.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = purgeGate(ctx, tx, scope); err != nil {
		return err
	}
	initial, err := json.Marshal(calendar.NewState())
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO calendar.fleets(id,tenant_id,user_id,state) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope.FleetID, scope.TenantID, scope.UserID, initial); err != nil {
		return classify(err)
	}
	query := `SELECT state FROM calendar.fleets WHERE id=$1 AND tenant_id=$2 AND user_id=$3`
	// The Fleet lock commits rule changes, occurrences and outbox facts together.
	if write {
		query += ` FOR UPDATE`
	}
	var data []byte
	if err = tx.QueryRow(ctx, query, scope.FleetID, scope.TenantID, scope.UserID).Scan(&data); err != nil {
		return classify(err)
	}
	state := calendar.NewState()
	if err = json.Unmarshal(data, state); err != nil {
		return err
	}
	if err = fn(state); err != nil {
		return err
	}
	if write {
		// Human/peer calls may carry a separately authorized schedule target.
		// Recheck that target inside the same lock as the cleanup barrier.
		targets := []string{}
		for _, job := range state.Jobs {
			if job.Status == "active" && job.AgentID != "" {
				targets = append(targets, job.AgentID)
			}
		}
		var blocked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calendar.purges WHERE fleet_id=$1 AND (whole_fleet OR agent_ids && $2::uuid[]))`, scope.FleetID, targets).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return application.ErrDenied
		}
		state.StageNotifications()
		data, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if len(data) > 64<<20 {
			return fmt.Errorf("%w: Fleet Calendar capacity reached", application.ErrConflict)
		}
		if _, err = tx.Exec(ctx, `UPDATE calendar.fleets SET state=$2,updated_at=clock_timestamp() WHERE id=$1`, scope.FleetID, data); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func classify(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrDenied
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "22P02":
			return application.ErrInvalid
		case "23505":
			return application.ErrConflict
		}
	}
	return err
}
