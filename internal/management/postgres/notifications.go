package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/management"
)

func notificationOwner(ctx context.Context, tx pgx.Tx, user, tenant string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT status='active' FROM management.memberships WHERE tenant_id=$1 AND user_id=$2`, tenant, user).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return management.ErrDenied
	}
	return err
}

func notificationPreferences(ctx context.Context, tx pgx.Tx, user, tenant string) (management.NotificationPreferences, error) {
	var p management.NotificationPreferences
	_, err := tx.Exec(ctx, `INSERT INTO management.notification_preferences(tenant_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, tenant, user)
	if err != nil {
		return p, err
	}
	err = tx.QueryRow(ctx, `SELECT version,completions,email FROM management.notification_preferences WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, tenant, user).Scan(&p.Version, &p.Completions, &p.Email)
	return p, err
}

func (d *Directory) NotificationPreferences(ctx context.Context, user, tenant string) (management.NotificationPreferences, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.NotificationPreferences{}, err
	}
	defer rollback(tx)
	if err := notificationOwner(ctx, tx, user, tenant); err != nil {
		return management.NotificationPreferences{}, err
	}
	p, err := notificationPreferences(ctx, tx, user, tenant)
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

func (d *Directory) ConfigureNotifications(ctx context.Context, user, tenant string, p management.NotificationPreferences) (management.NotificationPreferences, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return p, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenant); err != nil {
		return p, err
	}
	if err := notificationOwner(ctx, tx, user, tenant); err != nil {
		return p, err
	}
	old, err := notificationPreferences(ctx, tx, user, tenant)
	if err != nil {
		return p, err
	}
	if old.Version != p.Version {
		return p, management.ErrConflict
	}
	if p.Email {
		var verified bool
		if err := tx.QueryRow(ctx, `SELECT email_verified FROM management.users WHERE id=$1`, user).Scan(&verified); err != nil {
			return p, err
		}
		if !verified || !d.config.MailEnabled {
			return p, management.ErrInvalid
		}
	}
	p.Version++
	_, err = tx.Exec(ctx, `UPDATE management.notification_preferences SET version=$3,completions=$4,email=$5 WHERE tenant_id=$1 AND user_id=$2`, tenant, user, p.Version, p.Completions, p.Email)
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

// Only the authenticated private service adapter may record application facts.
// Historical facts survive revocation; current membership guards human visibility
// and each email attempt separately.
func (d *Directory) RecordNotification(ctx context.Context, event application.Event) error {
	if !event.Valid() {
		return management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, event.Scope.TenantID); err != nil {
		return err
	}
	if err := purgeGate(ctx, tx, event.Scope.FleetID, event.Scope.AgentID); err != nil {
		return err
	}
	var fleet string
	if err := tx.QueryRow(ctx, `SELECT id FROM management.fleets WHERE id=$1 AND tenant_id=$2 AND user_id=$3`, event.Scope.FleetID, event.Scope.TenantID, event.Scope.UserID).Scan(&fleet); err != nil {
		return classify(err)
	}
	if event.Scope.AgentID != "" {
		var found bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.agents WHERE id=$1 AND fleet_id=$2)`, event.Scope.AgentID, fleet).Scan(&found); err != nil {
			return err
		}
		if !found {
			return management.ErrDenied
		}
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	var previous string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM management.notifications WHERE application=$1 AND fleet_id=$2 AND event_id=$3`, event.Application, fleet, event.ID).Scan(&previous)
	if err == nil {
		if previous != hash {
			return management.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	p, err := notificationPreferences(ctx, tx, event.Scope.UserID, event.Scope.TenantID)
	if err != nil {
		return err
	}
	visible := event.Kind != "completed" || p.Completions
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO management.notifications(application,fleet_id,event_id,tenant_id,user_id,event,fingerprint,visible) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, event.Application, fleet, event.ID, event.Scope.TenantID, event.Scope.UserID, data, hash, visible).Scan(&id)
	if err != nil {
		return err
	}
	if visible && p.Email && d.config.MailEnabled {
		if err := d.queueNotificationMail(ctx, tx, id, event); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (d *Directory) Notifications(ctx context.Context, user, tenant string, before int64, limit int) (management.NotificationPage, error) {
	page := management.NotificationPage{Items: []management.Notification{}}
	if before < 0 || limit < 1 || limit > 100 {
		return page, management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return page, err
	}
	defer rollback(tx)
	if err := notificationOwner(ctx, tx, user, tenant); err != nil {
		return page, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM management.notifications WHERE tenant_id=$1 AND user_id=$2 AND visible AND read_at IS NULL`, tenant, user).Scan(&page.Unread); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT id,sequence,event,read_at IS NOT NULL FROM management.notifications WHERE tenant_id=$1 AND user_id=$2 AND visible AND ($3::bigint=0 OR sequence<$3) ORDER BY sequence DESC LIMIT $4`, tenant, user, before, limit+1)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var n management.Notification
		var data []byte
		var event application.Event
		if err := rows.Scan(&n.ID, &n.Sequence, &data, &n.Read); err != nil {
			rows.Close()
			return page, err
		}
		if err := json.Unmarshal(data, &event); err != nil {
			rows.Close()
			return page, err
		}
		n.Application, n.ResourceID, n.AgentID, n.Kind, n.Title, n.Summary, n.CreatedAt = event.Application, event.ResourceID, event.Scope.AgentID, event.Kind, event.Title, event.Summary, event.CreatedAt
		page.Items = append(page.Items, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = page.Items[limit-1].Sequence
	}
	return page, tx.Commit(ctx)
}

func (d *Directory) MarkNotification(ctx context.Context, user, tenant, id string, read bool) error {
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := notificationOwner(ctx, tx, user, tenant); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE management.notifications SET read_at=CASE WHEN $4 THEN clock_timestamp() ELSE NULL END WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND visible`, id, tenant, user, read)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return management.ErrDenied
	}
	return tx.Commit(ctx)
}
