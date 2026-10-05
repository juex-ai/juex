// Package postgres owns the Execution schema and its durable transitions.
package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

//go:embed schema.sql
var schema string

//go:embed events_schema.sql
var eventsSchema string

//go:embed hosted_schema.sql
var hostedSchema string

//go:embed hosted_storage_schema.sql
var hostedStorageSchema string

//go:embed observed_output_schema.sql
var observedOutputSchema string

//go:embed artifacts_schema.sql
var artifactsSchema string

//go:embed transfers_schema.sql
var transfersSchema string

//go:embed cancellations_schema.sql
var cancellationsSchema string

//go:embed transfer_progress_schema.sql
var transferProgressSchema string

//go:embed purge_schema.sql
var purgeSchema string

//go:embed recovery_schema.sql
var recoverySchema string

//go:embed retention_schema.sql
var retentionSchema string

//go:embed default_environment_schema.sql
var defaultEnvironmentSchema string

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.execution.migrations')); CREATE SCHEMA IF NOT EXISTS execution; CREATE TABLE IF NOT EXISTS execution.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		return err
	}
	migrations := []string{schema, eventsSchema, hostedSchema, hostedStorageSchema, observedOutputSchema, artifactsSchema, transfersSchema, cancellationsSchema, transferProgressSchema, purgeSchema, retentionSchema, recoverySchema, defaultEnvironmentSchema}
	rows, err := tx.Query(ctx, `SELECT version,checksum FROM execution.schema_versions ORDER BY version`)
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
			return fmt.Errorf("unsupported or modified Execution schema version %d", version)
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
		if _, err := tx.Exec(ctx, `INSERT INTO execution.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256([]byte(migrations[i])))); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('juex.execution.purge'))`); err != nil {
		rollback(tx)
		return nil, err
	}
	return tx, nil
}

// Single-statement mutations also need the service's isolation contract;
// deployments may configure a stricter default that rejects concurrent ticks.
func (s *Store) exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer rollback(tx)
	result, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func classify(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return execprotocol.ErrDenied
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			return execprotocol.ErrConflict
		case "22P02", "23514":
			return execprotocol.ErrInvalid
		case "23503":
			return execprotocol.ErrDenied
		}
	}
	return err
}
