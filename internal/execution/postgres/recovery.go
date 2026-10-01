package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

func RecoveryRevoke(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tx, err := pool.Begin(ctx)
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
	if _, err := tx.Exec(ctx, `UPDATE execution.environments SET status='revoked',version=version+1,grants='{}',online_until=NULL,connection_epoch=connection_epoch+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,environment_id,action,version) VALUES($1,$2,$3,'recovery.device_revoked',$4)`, device.TenantID, device.UserID, id, device.Version+1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
