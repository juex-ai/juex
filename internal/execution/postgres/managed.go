package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const managedColumns = `h.environment_id,h.agent_id,e.tenant_id,e.user_id,e.credential_hash,COALESCE(h.slot,0),COALESCE(h.memory_bytes,0),COALESCE(h.nano_cpus,0),h.running,h.last_activity,COALESCE(e.online_until>clock_timestamp(),false),EXISTS(SELECT 1 FROM execution.operations o WHERE o.environment_id=e.id AND (NOT o.acknowledged OR o.state IN ('waiting','dispatched','accepted','running'))),COALESCE(h.storage_identity::text,''),COALESCE(h.project_id,0),COALESCE(h.workspace_bytes,0),COALESCE(h.workspace_inodes,0),h.provisioned,h.purging,h.purge_data,h.backend,e.os,e.working_directory,h.home_directory,EXISTS(SELECT 1 FROM execution.operations o WHERE o.environment_id=e.id AND o.state IN ('dispatched','accepted','running','unknown'))`

func scanManaged(row pgx.Row) (execution.ManagedResource, error) {
	var h execution.ManagedResource
	err := row.Scan(&h.EnvironmentID, &h.AgentID, &h.TenantID, &h.UserID, &h.CredentialHash, &h.Slot, &h.Memory, &h.NanoCPUs, &h.Running, &h.LastActivity, &h.Online, &h.Busy, &h.StorageIdentity, &h.ProjectID, &h.WorkspaceBytes, &h.WorkspaceInodes, &h.Provisioned, &h.Purging, &h.PurgeData, &h.Backend, &h.OS, &h.WorkingDirectory, &h.HomeDirectory, &h.Unconfirmed)
	return h, err
}

func (s *Store) EnsureManaged(ctx context.Context, scope execution.Scope, candidate execution.ManagedResource) (execution.ManagedResource, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.ManagedResource{}, err
	}
	defer rollback(tx)
	if err := purgeGate(ctx, tx, scope.FleetID, scope.AgentID); err != nil {
		return execution.ManagedResource{}, err
	}
	// Serialize default provisioning with a concurrent user selection. A list
	// that began before the selection must not allocate an unused workspace.
	if err := lockDefaultEnvironment(ctx, tx, scope.AgentID); err != nil {
		return execution.ManagedResource{}, err
	}
	var selected string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT environment_id::text FROM execution.default_environments WHERE agent_id=$1),'')`, scope.AgentID).Scan(&selected); err != nil {
		return execution.ManagedResource{}, err
	}
	// Slot allocation and one-environment-per-Agent are one transaction.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.execution.managed_environments.allocate'))`); err != nil {
		return execution.ManagedResource{}, err
	}
	h, err := scanManaged(tx.QueryRow(ctx, `SELECT `+managedColumns+` FROM execution.managed_environments h JOIN execution.environments e ON e.id=h.environment_id WHERE h.agent_id=$1 FOR UPDATE OF e`, scope.AgentID))
	if err == nil {
		if selected != "" && selected != h.EnvironmentID {
			return h, execprotocol.ErrConflict
		}
		if h.TenantID != scope.TenantID || h.UserID != scope.UserID {
			return h, execprotocol.ErrDenied
		}
		if h.Backend != candidate.Backend {
			return h, execprotocol.ErrConflict
		}
		// Rejoining restores the owned workspace, never an old external-device
		// grant. Existing operations still carry their original authority epoch.
		if _, err := tx.Exec(ctx, `UPDATE execution.environments SET removal_epoch=$2 WHERE id=$1 AND fleet_id=$3 AND status='active'`, h.EnvironmentID, scope.RemovalEpoch, scope.FleetID); err != nil {
			return h, err
		}
		return h, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	if selected != "" {
		return h, execprotocol.ErrConflict
	}
	var slot *int
	if candidate.Backend == "gvisor" {
		var allocation int
		if err := tx.QueryRow(ctx, `SELECT n FROM generate_series(0,4095) n WHERE NOT EXISTS(SELECT 1 FROM execution.managed_environments h WHERE h.slot=n) ORDER BY n LIMIT 1`).Scan(&allocation); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				err = execprotocol.ErrQuota
			}
			return h, err
		}
		slot = &allocation
	}
	grants, _ := json.Marshal(map[string][]execprotocol.Capability{scope.AgentID: {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}})
	kind, name := "native", "Host workspace"
	if candidate.Backend == "gvisor" {
		kind, name = "hosted", "Hosted workspace"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.environments(id,tenant_id,user_id,fleet_id,kind,name,os,working_directory,credential_hash,removal_epoch,grants,ceiling) VALUES($1,$2,$3,$4,$8,$9,$10,$11,$5,$6,$7,$7)`, candidate.EnvironmentID, scope.TenantID, scope.UserID, scope.FleetID, candidate.CredentialHash, scope.RemovalEpoch, grants, kind, name, candidate.OS, candidate.WorkingDirectory); err != nil {
		return h, classify(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.managed_environments(environment_id,agent_id,slot,memory_bytes,nano_cpus,storage_identity,workspace_bytes,workspace_inodes,backend,home_directory,project_id) VALUES($1,$2,$3,NULLIF($4::bigint,0),NULLIF($5::bigint,0),NULLIF($6,'')::uuid,NULLIF($7::bigint,0),NULLIF($8::bigint,0),$9,$10,CASE WHEN $9='gvisor' THEN nextval('execution.managed_project_id') ELSE NULL END)`, candidate.EnvironmentID, scope.AgentID, slot, candidate.Memory, candidate.NanoCPUs, candidate.StorageIdentity, candidate.WorkspaceBytes, candidate.WorkspaceInodes, candidate.Backend, candidate.HomeDirectory); err != nil {
		return h, classify(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,actor_id,environment_id,agent_id,action,version) VALUES($1,$2,$3,$4,$5,'environment.provisioned',1)`, scope.TenantID, scope.UserID, scope.ActorID, candidate.EnvironmentID, scope.AgentID); err != nil {
		return h, err
	}
	h, err = scanManaged(tx.QueryRow(ctx, `SELECT `+managedColumns+` FROM execution.managed_environments h JOIN execution.environments e ON e.id=h.environment_id WHERE h.agent_id=$1`, scope.AgentID))
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) ManagedIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT environment_id FROM execution.managed_environments ORDER BY last_activity,environment_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) LockManaged(ctx context.Context, id string, action func(execution.ManagedResource) (execution.ManagedResult, error)) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Match operation admission's environment-first lock order. Admission can
	// wait for a bounded container stop, then persist work for the next wake.
	if _, err := tx.Exec(ctx, `SELECT id FROM execution.environments WHERE id=$1 FOR UPDATE`, id); err != nil {
		return err
	}
	h, err := scanManaged(tx.QueryRow(ctx, `SELECT `+managedColumns+` FROM execution.managed_environments h JOIN execution.environments e ON e.id=h.environment_id WHERE e.id=$1 FOR UPDATE OF h`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent reconciler may have finished permanent cleanup after
		// ManagedIDs took its snapshot.
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	result, err := action(h)
	if err != nil {
		return err
	}
	if result.Purged {
		if !h.Purging || !h.PurgeData || result.Running || h.Backend == "host" && h.Unconfirmed {
			return execprotocol.ErrConflict
		}
		if _, err = tx.Exec(ctx, `DELETE FROM execution.managed_environments WHERE environment_id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE execution.environments SET status='revoked',credential_hash='purged:'||id::text,online_until=NULL,name='',working_directory='' WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE execution.operations SET acknowledged=true WHERE environment_id=$1 AND ($2 OR state IN ('completed','failed','cancelled'))`, id, h.Backend == "gvisor"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.managed_environments SET running=$2,last_error=$3,provisioned=provisioned OR $4 WHERE environment_id=$1`, id, result.Running, result.Error, result.Provisioned); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ execution.ManagedRepository = (*Store)(nil)
