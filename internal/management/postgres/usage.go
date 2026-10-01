package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/management"
)

// Usage survives Fleet erasure, so authorization depends on membership, not on
// the continued existence of an Agent or its replacement Fleet.
func (d *Directory) AuthorizeUsage(ctx context.Context, actor, tenant, owner string) error {
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenant); err != nil {
		return err
	}
	m, err := membership(ctx, tx, tenant, actor)
	if err != nil {
		return err
	}
	if m.Status != management.Active || m.Role != management.Admin && owner != actor {
		return management.ErrDenied
	}
	if owner != "" {
		if _, err := membership(ctx, tx, tenant, owner); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
