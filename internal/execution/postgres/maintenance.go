package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func MaintenanceReport(ctx context.Context, pool *pgxpool.Pool) (maintenance.Report, error) {
	return maintenance.Inspect(ctx, pool, "execution", []maintenance.Query{
		{Kind: "operation", Busy: true, SQL: `SELECT o.environment_id::text||'/'||o.id,o.state FROM execution.operations o JOIN execution.environments e ON e.id=o.environment_id WHERE o.state IN ('dispatched','accepted','running','unknown') AND NOT (e.kind='hosted' AND e.credential_hash='purged:'||e.id::text) ORDER BY o.environment_id,o.id`},
		{Kind: "destroyed_hosted_operation", SQL: `SELECT o.environment_id::text||'/'||o.id,o.state FROM execution.operations o JOIN execution.environments e ON e.id=o.environment_id WHERE o.state IN ('dispatched','accepted','running','unknown') AND e.kind='hosted' AND e.credential_hash='purged:'||e.id::text ORDER BY o.environment_id,o.id`},
		{Kind: "transfer", Busy: true, SQL: `SELECT id::text,state FROM execution.transfers WHERE state IN ('accepted','unknown') ORDER BY id`},
		{Kind: "artifact", Busy: true, SQL: `SELECT id::text,state FROM execution.artifacts WHERE state='uploading' ORDER BY id`},
		{Kind: "queued_operation", SQL: `SELECT environment_id::text||'/'||id,state FROM execution.operations WHERE state='waiting' ORDER BY environment_id,id`},
		{Kind: "device_authority", SQL: `SELECT id::text,jsonb_build_object('tenant',tenant_id,'user',user_id,'fleet',fleet_id,'kind',kind,'status',status,'version',version,'grants',grants,'ceiling',ceiling)::text FROM execution.environments ORDER BY id`},
	})
}
