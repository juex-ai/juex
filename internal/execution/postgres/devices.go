package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const deviceColumns = `id,tenant_id,user_id,fleet_id,kind,name,os,working_directory,removal_epoch,grants,ceiling,status,version,journal_id,connection_epoch,last_seen,COALESCE(online_until>clock_timestamp(),false),COALESCE((SELECT last_error FROM execution.managed_environments WHERE environment_id=execution.environments.id),''),COALESCE((SELECT CASE WHEN running THEN 'starting' ELSE 'sleeping' END FROM execution.managed_environments WHERE environment_id=execution.environments.id),'offline'),managed`

func scanDevice(row pgx.Row) (execution.Device, error) {
	var device execution.Device
	err := row.Scan(&device.ID, &device.TenantID, &device.UserID, &device.FleetID, &device.Kind, &device.Name, &device.OS, &device.WorkingDirectory, &device.RemovalEpoch, &device.Grants, &device.Ceiling, &device.Status, &device.Version, &device.JournalID, &device.ConnectionEpoch, &device.LastSeen, &device.Online, &device.Error, &device.Availability, &device.Managed)
	if err != nil {
		return device, classify(err)
	}
	device.PermissionMode = "current_os_user"
	if device.Error != "" {
		device.Availability = "error"
	} else if device.Online {
		device.Availability = "ready"
	}
	if device.Kind == "hosted" {
		device.PermissionMode = "gvisor"
	}
	seen := map[execprotocol.Capability]bool{}
	for _, capabilities := range device.Grants {
		for _, capability := range capabilities {
			seen[capability] = true
		}
	}
	device.Capabilities = make([]execprotocol.Capability, 0, len(seen))
	for capability := range seen {
		device.Capabilities = append(device.Capabilities, capability)
	}
	sort.Slice(device.Capabilities, func(i, j int) bool { return device.Capabilities[i] < device.Capabilities[j] })
	return device, nil
}

func (s *Store) Device(ctx context.Context, id string) (execution.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1`, id))
}

func (s *Store) Devices(ctx context.Context, owner execution.OwnerScope) ([]execution.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE tenant_id=$1 AND user_id=$2 AND fleet_id=$3 ORDER BY created_at,id`, owner.TenantID, owner.UserID, owner.FleetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []execution.Device{}
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) AuthenticateDevice(ctx context.Context, token string) (execution.Device, error) {
	if len(token) < 32 || len(token) > 256 {
		return execution.Device{}, execprotocol.ErrDenied
	}
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE credential_hash=$1`, execution.Digest(token)))
}

func (s *Store) Revoke(ctx context.Context, id, actor string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if device.Status == "revoked" {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.environments SET status='revoked',version=version+1,grants='{}' WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,actor_id,environment_id,action,version) VALUES($1,$2,$3,$4,'device.revoked',$5)`, device.TenantID, device.UserID, actor, id, device.Version+1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SetGrants(ctx context.Context, id string, version int64, grants map[string][]execprotocol.Capability, actor string) (execution.Device, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Device{}, err
	}
	defer rollback(tx)
	prior, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return execution.Device{}, err
	}
	if err = purgeGate(ctx, tx, prior.FleetID, ""); err != nil {
		return execution.Device{}, err
	}
	for agent := range grants {
		if err = purgeGate(ctx, tx, prior.FleetID, agent); err != nil {
			return execution.Device{}, err
		}
	}
	encoded, err := json.Marshal(grants)
	if err != nil {
		return execution.Device{}, err
	}
	device, err := scanDevice(tx.QueryRow(ctx, `UPDATE execution.environments SET grants=$3,version=version+1 WHERE id=$1 AND version=$2 AND status='active' RETURNING `+deviceColumns, id, version, encoded))
	if err != nil {
		if errors.Is(err, execprotocol.ErrDenied) {
			err = execprotocol.ErrConflict
		}
		return device, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,actor_id,environment_id,action,version) VALUES($1,$2,$3,$4,'device.grants_changed',$5)`, device.TenantID, device.UserID, actor, id, device.Version); err != nil {
		return device, err
	}
	return device, tx.Commit(ctx)
}

var _ execution.Repository = (*Store)(nil)
