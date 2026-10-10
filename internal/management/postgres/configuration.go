package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
)

func tenantSettings(ctx context.Context, tx pgx.Tx, tenant string) (management.ConfigurationLayer, error) {
	var result management.ConfigurationLayer
	err := tx.QueryRow(ctx, `SELECT configuration,configuration_version FROM management.tenants WHERE id=$1`, tenant).Scan(&result.Declaration, &result.Version)
	return result, classify(err)
}

func configurationLayers(ctx context.Context, tx pgx.Tx, tenant string, agent management.Agent) (management.ConfigurationLayers, error) {
	layers := management.ConfigurationLayers{Agent: management.ConfigurationLayer{Declaration: agent.Configuration, Version: agent.Version}}
	if agent.WorkspaceConfiguration != nil {
		layers.Workspace = agent.WorkspaceConfiguration.ConfigurationLayer
	}
	err := tx.QueryRow(ctx, `SELECT t.configuration,t.configuration_version,f.configuration,f.version FROM management.tenants t JOIN management.fleets owned ON owned.tenant_id=t.id JOIN management.fleet_settings f ON f.fleet_id=owned.id WHERE t.id=$1 AND f.fleet_id=$2`, tenant, agent.FleetID).Scan(&layers.Tenant.Declaration, &layers.Tenant.Version, &layers.Fleet.Declaration, &layers.Fleet.Version)
	return layers, classify(err)
}

func enabledConfiguration(ctx context.Context, tx pgx.Tx, tenant string, value management.Configuration) error {
	if err := value.Validate(); err != nil {
		return err
	}
	for _, id := range value.Models {
		if err := enabledModel(ctx, tx, tenant, id); err != nil {
			return err
		}
	}
	return nil
}

// The caller holds the Tenant lock. Capture all affected policies before the
// declaration changes, then fence only Agents whose effective grants shrink.
func effectivePolicies(ctx context.Context, tx pgx.Tx, tenant, fleet string) (map[string]agentpolicy.Policy, error) {
	rows, err := tx.Query(ctx, `SELECT a.id,a.configuration,a.workspace_configuration,f.configuration,t.configuration FROM management.agents a JOIN management.fleets owned ON owned.id=a.fleet_id JOIN management.fleet_settings f ON f.fleet_id=owned.id JOIN management.tenants t ON t.id=owned.tenant_id WHERE t.id=$1 AND ($2='' OR owned.id=NULLIF($2,'')::uuid) ORDER BY a.id`, tenant, fleet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]agentpolicy.Policy{}
	for rows.Next() {
		var id string
		var layers management.ConfigurationLayers
		var workspace *management.WorkspaceConfiguration
		if err := rows.Scan(&id, &layers.Agent.Declaration, &workspace, &layers.Fleet.Declaration, &layers.Tenant.Declaration); err != nil {
			return nil, err
		}
		if workspace != nil {
			layers.Workspace = workspace.ConfigurationLayer
		}
		result[id] = layers.Resolve().Policy()
	}
	return result, rows.Err()
}

func fenceConfigurationChanges(ctx context.Context, tx pgx.Tx, tenant, fleet string, before map[string]agentpolicy.Policy) error {
	after, err := effectivePolicies(ctx, tx, tenant, fleet)
	if err != nil {
		return err
	}
	var revoked []string
	for id, policy := range after {
		if policy.Restricts(before[id]) {
			revoked = append(revoked, id)
		}
	}
	if len(revoked) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE management.agents SET execution_epoch=execution_epoch+1 WHERE id=ANY($1::uuid[])`, revoked)
	return err
}

func (d *Directory) TenantSettings(ctx context.Context, actor, tenant string) (management.ConfigurationLayer, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.ConfigurationLayer{}, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenant); err != nil {
		return management.ConfigurationLayer{}, err
	}
	member, err := membership(ctx, tx, tenant, actor)
	if err != nil {
		return management.ConfigurationLayer{}, err
	}
	if member.Status != management.Active {
		return management.ConfigurationLayer{}, management.ErrDenied
	}
	result, err := tenantSettings(ctx, tx, tenant)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (d *Directory) ConfigureTenantSettings(ctx context.Context, actor, tenant string, value management.ConfigurationLayer) (management.ConfigurationLayer, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return value, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenant); err != nil {
		return value, err
	}
	if err := requireAdmin(ctx, tx, tenant, actor); err != nil {
		return value, err
	}
	if err := enabledConfiguration(ctx, tx, tenant, value.Declaration); err != nil {
		return value, err
	}
	before, err := effectivePolicies(ctx, tx, tenant, "")
	if err != nil {
		return value, err
	}
	err = tx.QueryRow(ctx, `UPDATE management.tenants SET configuration=$2,configuration_version=configuration_version+1 WHERE id=$1 AND configuration_version=$3 RETURNING configuration_version`, tenant, value.Declaration, value.Version).Scan(&value.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, management.ErrConflict
	}
	if err != nil {
		return value, err
	}
	if err := fenceConfigurationChanges(ctx, tx, tenant, "", before); err != nil {
		return value, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.audit(tenant_id,actor_id,action,membership_version,before_role,before_status,after_role,after_status,resource_version) VALUES($1,$2,'tenant.configured',0,'','','','',$3)`, tenant, actor, value.Version); err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}
