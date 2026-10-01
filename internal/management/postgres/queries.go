package postgres

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/management"
)

func (d *Directory) Tenants(ctx context.Context, actorID string) ([]management.TenantAccess, error) {
	rows, err := d.pool.Query(ctx, `SELECT t.id,t.name,m.role,f.id FROM management.memberships m
	JOIN management.tenants t ON t.id=m.tenant_id JOIN management.fleets f ON f.tenant_id=m.tenant_id AND f.user_id=m.user_id
	WHERE m.user_id=$1 AND m.status='active' ORDER BY t.created_at,t.id`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []management.TenantAccess{}
	for rows.Next() {
		var v management.TenantAccess
		if err := rows.Scan(&v.ID, &v.Name, &v.Role, &v.FleetID); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (d *Directory) Members(ctx context.Context, actorID, tenantID string) ([]management.MemberView, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	if err := requireAdmin(ctx, tx, tenantID, actorID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT u.id,u.email,u.email_verified,m.tenant_id,m.user_id,m.role,m.status,m.version,COALESCE(f.id::text,'')
	FROM management.memberships m JOIN management.users u ON u.id=m.user_id
	LEFT JOIN management.fleets f ON f.tenant_id=m.tenant_id AND f.user_id=m.user_id WHERE m.tenant_id=$1 ORDER BY u.email`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []management.MemberView{}
	for rows.Next() {
		var v management.MemberView
		if err := rows.Scan(&v.ID, &v.Email, &v.EmailVerified, &v.TenantID, &v.UserID, &v.Role, &v.Status, &v.Version, &v.FleetID); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return result, tx.Commit(ctx)
}

func (d *Directory) Invitations(ctx context.Context, actorID, tenantID string) ([]management.InvitationView, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	if err := requireAdmin(ctx, tx, tenantID, actorID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT i.id,i.tenant_id,i.email,i.role,i.expires_at,i.token_cipher,
	COALESCE((SELECT CASE WHEN delivered_at IS NOT NULL THEN 'sent' WHEN attempts>=8 THEN 'failed' WHEN attempts>0 THEN 'retrying' ELSE 'queued' END
	FROM management.mail_outbox WHERE invitation_id=i.id ORDER BY created_at DESC,id DESC LIMIT 1),'manual')
	FROM management.invitations i WHERE tenant_id=$1 AND consumed_at IS NULL AND expires_at>clock_timestamp() ORDER BY email`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []management.InvitationView{}
	for rows.Next() {
		var v management.InvitationView
		var cipher []byte
		if err := rows.Scan(&v.ID, &v.TenantID, &v.Email, &v.Role, &v.ExpiresAt, &cipher, &v.DeliveryStatus); err != nil {
			return nil, err
		}
		if len(cipher) > 0 {
			token, err := d.config.Secrets.Open("invitation:"+v.TenantID+":"+v.Email, cipher)
			if err != nil {
				return nil, err
			}
			v.Link = d.config.PublicURL + "/join#token=" + string(token)
		}
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return result, tx.Commit(ctx)
}

func (d *Directory) PreviewInvitation(ctx context.Context, token string) (management.InvitationPreview, error) {
	hash := sha256.Sum256([]byte(token))
	var v management.InvitationPreview
	err := d.pool.QueryRow(ctx, `SELECT i.id,i.tenant_id,i.email,i.role,i.expires_at,t.name,
	EXISTS(SELECT 1 FROM management.users u WHERE u.email=i.email) FROM management.invitations i
	JOIN management.tenants t ON t.id=i.tenant_id WHERE i.token_hash=$1 AND i.consumed_at IS NULL AND i.expires_at>clock_timestamp()`, hash[:]).Scan(&v.ID, &v.TenantID, &v.Email, &v.Role, &v.ExpiresAt, &v.TenantName, &v.RequiresLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, management.ErrInvitation
	}
	return v, err
}

func (d *Directory) queueMail(ctx context.Context, tx pgx.Tx, email, subject, body string, invitationID ...string) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return err
	}
	cipher, err := d.config.Secrets.Seal("mail:"+id, []byte(body))
	if err != nil {
		return err
	}
	var invitation *string
	if len(invitationID) > 0 {
		invitation = &invitationID[0]
	}
	_, err = tx.Exec(ctx, `INSERT INTO management.mail_outbox(id,recipient,subject,body_cipher,invitation_id) VALUES($1,$2,$3,$4,$5)`, id, email, subject, cipher, invitation)
	return err
}
