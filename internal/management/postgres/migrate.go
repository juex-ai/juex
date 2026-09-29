package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var initialSchema string

// Migrate runs explicit, transactional Management migrations. Runtime startup
// must not infer a business schema from files or silently rewrite old versions.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.management.migrations'))`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS management;
		CREATE TABLE IF NOT EXISTS management.schema_versions (version integer PRIMARY KEY, checksum text NOT NULL)`); err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(initialSchema)))
	rows, err := tx.Query(ctx, `SELECT version, checksum FROM management.schema_versions ORDER BY version`)
	if err != nil {
		return err
	}
	installed := false
	for rows.Next() {
		var version int
		var stored string
		if err := rows.Scan(&version, &stored); err != nil {
			rows.Close()
			return err
		}
		if version != 1 || stored != checksum {
			rows.Close()
			return fmt.Errorf("unsupported or modified Management schema version %d", version)
		}
		installed = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !installed {
		if _, err := tx.Exec(ctx, initialSchema); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO management.schema_versions (version, checksum) VALUES (1, $1)`, checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
