// Package postgres owns the independent Memory schema.
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
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/memory"
)

//go:embed schema.sql
var schema string

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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.memory.migrations')); CREATE SCHEMA IF NOT EXISTS memory; CREATE TABLE IF NOT EXISTS memory.schema_versions(version integer PRIMARY KEY, checksum text NOT NULL)`); err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(schema)))
	var stored string
	err = tx.QueryRow(ctx, `SELECT checksum FROM memory.schema_versions WHERE version=1`).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = tx.Exec(ctx, schema); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO memory.schema_versions VALUES(1,$1)`, checksum); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if stored != checksum {
		return errors.New("modified Memory schema")
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM memory.schema_versions`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("unsupported Memory schema")
	}
	return tx.Commit(ctx)
}

func (s *Store) View(ctx context.Context, scope application.Scope, fn func(*memory.State) error) error {
	return s.transaction(ctx, scope, false, fn)
}
func (s *Store) Update(ctx context.Context, scope application.Scope, fn func(*memory.State) error) error {
	return s.transaction(ctx, scope, true, fn)
}

func (s *Store) transaction(ctx context.Context, scope application.Scope, write bool, fn func(*memory.State) error) error {
	if !scope.Valid() {
		return application.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	initial, err := json.Marshal(memory.NewState())
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO memory.fleets(id,tenant_id,user_id,state) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope.FleetID, scope.TenantID, scope.UserID, initial); err != nil {
		return classify(err)
	}
	query := `SELECT state FROM memory.fleets WHERE id=$1 AND tenant_id=$2 AND user_id=$3`
	// Whole-Fleet validation sees unmodified facts too. One short row lock avoids
	// write skew between different Entry IDs without locking any other Fleet.
	if write {
		query += ` FOR UPDATE`
	}
	var data []byte
	if err = tx.QueryRow(ctx, query, scope.FleetID, scope.TenantID, scope.UserID).Scan(&data); err != nil {
		return classify(err)
	}
	state := memory.NewState()
	if err = json.Unmarshal(data, state); err != nil {
		return err
	}
	if err = fn(state); err != nil {
		return err
	}
	if write {
		data, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if len(data) > 64<<20 {
			return fmt.Errorf("%w: Fleet Memory capacity reached", application.ErrConflict)
		}
		if _, err = tx.Exec(ctx, `UPDATE memory.fleets SET state=$2,updated_at=clock_timestamp() WHERE id=$1`, scope.FleetID, data); err != nil {
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
