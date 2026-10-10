package postgres

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/management"
)

// An absent policy inherits the deployment catalog. An explicit empty allowlist
// denies all models; it must not accidentally fall back to inheritance.
const modelVisible = `(NOT EXISTS(SELECT 1 FROM management.tenant_model_policy p WHERE p.tenant_id=$1 AND NOT p.inherit) OR EXISTS(SELECT 1 FROM management.tenant_model_access a WHERE a.tenant_id=$1 AND a.model_id=m.id))`

func (d *Directory) SetTenantModels(ctx context.Context, tenant string, inherit bool, models []string) error {
	if inherit && len(models) > 0 || len(models) > 1024 {
		return management.ErrInvalid
	}
	models = slices.Clone(models)
	slices.Sort(models)
	if len(slices.Compact(slices.Clone(models))) != len(models) {
		return management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockTenant(ctx, tx, tenant); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM management.models WHERE id=ANY($1::uuid[])`, models).Scan(&count); err != nil {
		return classify(err)
	}
	if count != len(models) {
		return management.ErrInvalid
	}
	before, err := visibleModelIDs(ctx, tx, tenant)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management.tenant_model_policy(tenant_id,inherit) VALUES($1,$2) ON CONFLICT(tenant_id) DO UPDATE SET inherit=EXCLUDED.inherit`, tenant, inherit); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM management.tenant_model_access WHERE tenant_id=$1`, tenant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management.tenant_model_access(tenant_id,model_id) SELECT $1,unnest($2::uuid[])`, tenant, models); err != nil {
		return err
	}
	after, err := visibleModelIDs(ctx, tx, tenant)
	if err != nil {
		return err
	}
	var changed []string
	for _, id := range before {
		if !slices.Contains(after, id) {
			changed = append(changed, id)
		}
	}
	for _, id := range after {
		if !slices.Contains(before, id) {
			changed = append(changed, id)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.tenant_model_epochs(tenant_id,model_id,epoch) SELECT $1,unnest($2::uuid[]),2 ON CONFLICT(tenant_id,model_id) DO UPDATE SET epoch=tenant_model_epochs.epoch+1`, tenant, changed); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES('model.tenant_access',$1)`, tenant); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func visibleModelIDs(ctx context.Context, tx pgx.Tx, tenant string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT m.id FROM management.models m WHERE `+modelVisible, tenant)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
