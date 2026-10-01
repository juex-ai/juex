package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/management"
)

// RecoverySuspend is an offline operator action. It never impersonates a user
// and retains the last-active-administrator invariant.
func RecoverySuspend(ctx context.Context, pool *pgxpool.Pool, tenant, user string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenant); err != nil {
		return err
	}
	member, err := membership(ctx, tx, tenant, user)
	if err != nil {
		return err
	}
	if member.Status != management.Active {
		return tx.Commit(ctx)
	}
	if member.Role == management.Admin {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM management.memberships WHERE tenant_id=$1 AND role='admin' AND status='active'`, tenant).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return management.ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE management.memberships SET status='suspended',version=version+1,execution_epoch=execution_epoch+1 WHERE tenant_id=$1 AND user_id=$2`, tenant, user); err != nil {
		return err
	}
	fleet, err := fleetFor(ctx, tx, tenant, user)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES('recovery.membership_suspended',$1)`, fleet.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
