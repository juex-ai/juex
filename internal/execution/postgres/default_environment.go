package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (s *Store) DefaultEnvironment(ctx context.Context, scope execution.Scope) (execution.DefaultEnvironment, error) {
	var value execution.DefaultEnvironment
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(environment_id::text,''),working_directory,version FROM execution.default_environments WHERE agent_id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&value.EnvironmentID, &value.WorkingDirectory, &value.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return value, err
}

func (s *Store) SetDefaultEnvironment(ctx context.Context, scope execution.Scope, value execution.DefaultEnvironment) (execution.DefaultEnvironment, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return value, err
	}
	defer rollback(tx)
	if err = purgeGate(ctx, tx, scope.FleetID, scope.AgentID); err != nil {
		return value, err
	}
	if err = lockDefaultEnvironment(ctx, tx, scope.AgentID); err != nil {
		return value, err
	}
	if value.EnvironmentID != "" {
		device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, value.EnvironmentID))
		if err != nil {
			return value, err
		}
		if device.Status != "active" || device.TenantID != scope.TenantID || device.UserID != scope.UserID || device.FleetID != scope.FleetID || device.RemovalEpoch != scope.RemovalEpoch || len(device.Grants[scope.AgentID]) == 0 {
			return value, execprotocol.ErrDenied
		}
	}
	var next int64
	err = tx.QueryRow(ctx, `INSERT INTO execution.default_environments(agent_id,tenant_id,user_id,fleet_id,environment_id,working_directory,version)
        SELECT $1,$2,$3,$4,NULLIF($5,'')::uuid,$6,1 WHERE $7::bigint=0
        ON CONFLICT (agent_id) DO NOTHING RETURNING version`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID, value.EnvironmentID, value.WorkingDirectory, value.Version).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) && value.Version > 0 {
		err = tx.QueryRow(ctx, `UPDATE execution.default_environments SET environment_id=NULLIF($5,'')::uuid,working_directory=$6,version=version+1
            WHERE agent_id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4 AND version=$7 RETURNING version`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID, value.EnvironmentID, value.WorkingDirectory, value.Version).Scan(&next)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return value, execprotocol.ErrConflict
	}
	if err != nil {
		return value, classify(err)
	}
	value.Version = next
	if _, err = tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,actor_id,environment_id,agent_id,action,version) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,'environment.default_changed',$6)`, scope.TenantID, scope.UserID, scope.ActorID, value.EnvironmentID, scope.AgentID, next); err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}

func lockDefaultEnvironment(ctx context.Context, tx pgx.Tx, agent string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.execution.default.'||$1))`, agent)
	return err
}
