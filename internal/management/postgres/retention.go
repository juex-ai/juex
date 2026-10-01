package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/management"
)

// PruneAudit only removes bounded historical facts. Authority epochs, sessions,
// jobs and recovery receipts have independent lifecycles.
func (d *Directory) PruneAudit(ctx context.Context, days int) error {
	if days < 1 || days > 3650 {
		return management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, table := range []string{"audit", "identity_audit", "operator_audit"} {
		if _, err := tx.Exec(ctx, `DELETE FROM management.`+table+` WHERE id IN (SELECT id FROM management.`+table+` WHERE created_at<clock_timestamp()-make_interval(days=>$1) ORDER BY created_at,id LIMIT 10000)`, days); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
