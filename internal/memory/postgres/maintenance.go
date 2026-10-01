package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func MaintenanceReport(ctx context.Context, pool *pgxpool.Pool) (maintenance.Report, error) {
	return maintenance.Inspect(ctx, pool, "memory", []maintenance.Query{
		{Kind: "review", SQL: `SELECT f.id::text||'/'||x.key,COALESCE(x.value->'receipt'->>'state','pending') FROM memory.fleets f CROSS JOIN LATERAL jsonb_each(COALESCE(state->'reviews','{}')) x WHERE NOT COALESCE((x.value->>'worker_finished')::boolean,false) ORDER BY f.id,x.key`},
	})
}
