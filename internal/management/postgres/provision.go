package postgres

import (
	"context"
	"time"

	"github.com/juex-ai/juex/internal/management"
)

type ProvisionedTenant struct {
	Tenant   management.Tenant `json:"tenant"`
	SetupURL string            `json:"setup_url,omitempty"`
}

// ProvisionTenant is an operator-only operation. Existing accounts keep their
// credentials and receive no password setup capability.
func (a *Auth) ProvisionTenant(ctx context.Context, name, email string) (ProvisionedTenant, error) {
	email, err := management.NormalizeEmail(email)
	if err != nil {
		return ProvisionedTenant{}, err
	}
	tx, err := a.d.begin(ctx)
	if err != nil {
		return ProvisionedTenant{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.management.bootstrap'))`); err != nil {
		return ProvisionedTenant{}, err
	}
	var userID string
	if err := tx.QueryRow(ctx, `INSERT INTO management.users(email) VALUES($1)
	ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING id`, email).Scan(&userID); err != nil {
		return ProvisionedTenant{}, err
	}
	tenant, err := createTenant(ctx, tx, name, userID)
	if err != nil {
		return ProvisionedTenant{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.passwords WHERE user_id=$1)`, userID).Scan(&exists); err != nil {
		return ProvisionedTenant{}, err
	}
	result := ProvisionedTenant{Tenant: tenant}
	if !exists {
		token, err := issueToken(ctx, tx, userID, "bootstrap", 30*time.Minute)
		if err != nil {
			return ProvisionedTenant{}, err
		}
		result.SetupURL = a.d.config.PublicURL + "/set-password#token=" + token
	}
	if err := identityAudit(ctx, tx, userID, "operator.tenant_provisioned"); err != nil {
		return ProvisionedTenant{}, err
	}
	return result, tx.Commit(ctx)
}
