package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func MaintenanceReport(ctx context.Context, pool *pgxpool.Pool) (maintenance.Report, error) {
	return maintenance.Inspect(ctx, pool, "management", []maintenance.Query{
		{Kind: "mail", SQL: `SELECT id::text,'pending' FROM management.mail_outbox WHERE delivered_at IS NULL ORDER BY id`},
		{Kind: "membership_authority", SQL: `SELECT tenant_id::text||'/'||user_id::text,status||':'||role FROM management.memberships ORDER BY tenant_id,user_id`},
		{Kind: "purge", SQL: `SELECT id::text,state FROM management.purges WHERE state<>'completed' ORDER BY id`},
	})
}
