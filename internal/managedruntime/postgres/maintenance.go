package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func MaintenanceReport(ctx context.Context, pool *pgxpool.Pool) (maintenance.Report, error) {
	return maintenance.Inspect(ctx, pool, "runtime", []maintenance.Query{
		{Kind: "provider_attempt", Busy: true, SQL: `SELECT id::text,state FROM runtime.attempts WHERE state='started' ORDER BY id`},
		{Kind: "input", SQL: `SELECT id::text,state FROM runtime.inputs WHERE state IN ('queued','active','held') ORDER BY id`},
		{Kind: "tool", SQL: `SELECT id::text,state FROM runtime.tools WHERE state IN ('pending','waiting','unknown') OR operation_live ORDER BY id`},
		{Kind: "hook", SQL: `SELECT id::text,state FROM runtime.hooks WHERE state IN ('pending','waiting','unknown') ORDER BY id`},
	})
}
