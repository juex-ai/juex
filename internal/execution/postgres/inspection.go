package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
)

func (s *Store) InspectEnvironments(ctx context.Context, scope execution.Scope) (binding execution.DefaultEnvironment, devices []execution.Device, observed time.Time, err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return
	}
	defer rollback(tx)
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&observed); err != nil {
		return
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE(environment_id::text,''),working_directory,version FROM execution.default_environments WHERE agent_id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&binding.EnvironmentID, &binding.WorkingDirectory, &binding.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return
	}
	rows, err := tx.Query(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE tenant_id=$1 AND user_id=$2 AND fleet_id=$3 ORDER BY created_at,id`, scope.TenantID, scope.UserID, scope.FleetID)
	if err != nil {
		return
	}
	devices = []execution.Device{}
	for rows.Next() {
		var device execution.Device
		device, err = scanDevice(rows)
		if err != nil {
			rows.Close()
			return
		}
		devices = append(devices, device)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return
	}
	err = tx.Commit(ctx)
	return
}
