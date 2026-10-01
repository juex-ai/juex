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

//go:embed auth_schema.sql
var authSchema string

//go:embed mail_schema.sql
var mailSchema string

//go:embed resources_schema.sql
var resourcesSchema string

//go:embed authority_schema.sql
var authoritySchema string

//go:embed workers_schema.sql
var workersSchema string

//go:embed model_policy_schema.sql
var modelPolicySchema string

//go:embed applications_schema.sql
var applicationsSchema string

//go:embed notifications_schema.sql
var notificationsSchema string

//go:embed purge_schema.sql
var purgeSchema string

//go:embed retention_schema.sql
var retentionSchema string

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
	migrations := []string{initialSchema, authSchema, mailSchema, resourcesSchema, authoritySchema, modelPolicySchema, workersSchema, applicationsSchema, notificationsSchema, purgeSchema, retentionSchema}
	rows, err := tx.Query(ctx, `SELECT version, checksum FROM management.schema_versions ORDER BY version`)
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
			return fmt.Errorf("unsupported or modified Management schema version %d", version)
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
		if _, err := tx.Exec(ctx, `INSERT INTO management.schema_versions (version, checksum) VALUES ($1, $2)`, i+1, fmt.Sprintf("%x", sha256.Sum256([]byte(migrations[i])))); err != nil {
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
