package managed

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

// InspectMaintenance is an offline operator composition. Each service owns its
// inventory queries; opening this connection never runs business migrations.
func InspectMaintenance(ctx context.Context, address string, offline bool) ([]maintenance.Report, error) {
	pool, err := openDatabase(ctx, address)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	if offline {
		if err := requireOffline(ctx, pool); err != nil {
			return nil, err
		}
	}
	reports := make([]maintenance.Report, 0, 5)
	for _, inspect := range []func(context.Context, *pgxpool.Pool) (maintenance.Report, error){managementpg.MaintenanceReport, runtimepg.MaintenanceReport, executionpg.MaintenanceReport, memorypg.MaintenanceReport, calendarpg.MaintenanceReport} {
		r, err := inspect(ctx, pool)
		if err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, nil
}

func requireOffline(ctx context.Context, pool *pgxpool.Pool) error {
	var others int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend' AND pid<>pg_backend_pid()`).Scan(&others); err != nil {
		return err
	}
	if others != 0 {
		return errors.New("offline maintenance requires all other business database connections to close")
	}
	return nil
}
