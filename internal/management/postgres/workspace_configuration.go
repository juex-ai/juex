package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"path"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

// ConfigureWorkspace accepts only the composition layer's inspected snapshot.
// Reading Execution takes place before this Tenant transaction, never inside it.
func (d *Directory) ConfigureWorkspace(ctx context.Context, actor, tenant, agentID string, version int64, snapshot *management.WorkspaceConfiguration) (management.Agent, error) {
	if snapshot != nil {
		environment, err := uuid.Parse(snapshot.EnvironmentID)
		hash, hashErr := hex.DecodeString(snapshot.SHA256)
		if err != nil || environment == uuid.Nil || environment.String() != snapshot.EnvironmentID || hashErr != nil || len(hash) != 32 || hex.EncodeToString(hash) != snapshot.SHA256 || !path.IsAbs(snapshot.WorkingDirectory) || len(snapshot.WorkingDirectory) > 4096 || snapshot.OperationID == "" || snapshot.AuthorizationVersion < 1 || snapshot.ReadStartedAt.IsZero() || (execprotocol.WorkspaceQuery{Path: snapshot.Path, Read: true}).Validate() != nil {
			return management.Agent{}, management.ErrInvalid
		}
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Agent{}, err
	}
	defer rollback(tx)
	owner, err := agentOwner(ctx, tx, tenant, agentID)
	if err != nil {
		return management.Agent{}, err
	}
	fleet, member, err := authorizeFleet(ctx, tx, actor, tenant, owner, true)
	if err != nil {
		return management.Agent{}, err
	}
	prior, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE id=$1`, agentID))
	if err != nil {
		return management.Agent{}, err
	}
	layers, err := configurationLayers(ctx, tx, tenant, prior)
	if err != nil {
		return management.Agent{}, err
	}
	before := layers.Resolve().Policy()
	if snapshot == nil {
		layers.Workspace = management.ConfigurationLayer{}
	} else {
		value := *snapshot
		value.Declaration = value.Declaration.Clone()
		if err := enabledConfiguration(ctx, tx, tenant, value.Declaration); err != nil {
			return management.Agent{}, err
		}
		value.Version = layers.Workspace.Version + 1
		snapshot = &value
		layers.Workspace = value.ConfigurationLayer
	}
	revoke := layers.Resolve().Policy().Restricts(before)
	result, err := scanAgent(tx.QueryRow(ctx, `UPDATE management.agents SET workspace_configuration=$3,version=version+1,execution_epoch=execution_epoch+CASE WHEN $4 THEN 1 ELSE 0 END,updated_at=clock_timestamp() WHERE id=$1 AND version=$2 AND status='active' AND NOT purging RETURNING `+agentColumns, agentID, version, snapshot, revoke))
	if errors.Is(err, management.ErrDenied) {
		return result, management.ErrConflict
	}
	if err != nil {
		return result, err
	}
	if err := recordResource(ctx, tx, actor, fleet, member, "agent.workspace_configured", agentID, result.Version); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
