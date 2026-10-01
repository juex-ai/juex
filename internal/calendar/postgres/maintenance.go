package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func MaintenanceReport(ctx context.Context, pool *pgxpool.Pool) (maintenance.Report, error) {
	return maintenance.Inspect(ctx, pool, "calendar", []maintenance.Query{
		{Kind: "schedule", SQL: `SELECT f.id::text||'/'||x.key,COALESCE(x.value->>'status','') FROM calendar.fleets f CROSS JOIN LATERAL jsonb_each(COALESCE(state->'jobs','{}')) x ORDER BY f.id,x.key`},
		{Kind: "occurrence", SQL: `SELECT f.id::text||'/'||x.key,COALESCE(x.value->>'state','pending') FROM calendar.fleets f CROSS JOIN LATERAL jsonb_each(COALESCE(state->'deliveries','{}')) x WHERE NOT COALESCE((x.value->>'settled')::boolean,false) ORDER BY f.id,x.key`},
	})
}
