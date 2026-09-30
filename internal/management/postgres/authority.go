package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/management"
)

func (d *Directory) AuthorizeFleet(ctx context.Context, actor, tenant, owner string, execute bool) (management.FleetAuthority, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.FleetAuthority{}, err
	}
	defer rollback(tx)
	fleet, member, err := authorizeFleet(ctx, tx, actor, tenant, owner, execute)
	if err != nil {
		return management.FleetAuthority{}, err
	}
	result := management.FleetAuthority{Fleet: fleet, ActorID: actor, ActorAuthorizationEpoch: member.ExecutionEpoch, MembershipVersion: member.Version, MembershipExecutionEpoch: member.ExecutionEpoch, CanExecute: member.Status == management.Active}
	if err := tx.QueryRow(ctx, `SELECT m.removal_epoch,u.email,t.name FROM management.memberships m JOIN management.users u ON u.id=m.user_id JOIN management.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 AND m.user_id=$2`, tenant, owner).Scan(&result.RemovalEpoch, &result.OwnerEmail, &result.TenantName); err != nil {
		return result, err
	}
	result.Settings, err = fleetSettings(ctx, tx, fleet.ID)
	if err != nil {
		return result, err
	}
	if actor != owner {
		acting, err := membership(ctx, tx, tenant, actor)
		if err != nil {
			return result, err
		}
		result.ActorAuthorizationEpoch = acting.Version
		if !execute {
			if err := record(ctx, tx, actor, fleet, "fleet.read", member, member, false); err != nil {
				return result, err
			}
		}
	}
	return result, tx.Commit(ctx)
}
