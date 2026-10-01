package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func (d *Directory) queueNotificationMail(ctx context.Context, tx pgx.Tx, notification string, event application.Event) error {
	var recipient string
	err := tx.QueryRow(ctx, `SELECT u.email FROM management.users u JOIN management.memberships m ON m.user_id=u.id WHERE u.id=$1 AND m.tenant_id=$2 AND m.status='active' AND u.email_verified`, event.Scope.UserID, event.Scope.TenantID).Scan(&recipient)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	id := uuid.NewString()
	// External mail intentionally contains no app-supplied title, evidence or
	// conversation content. The authenticated Inbox remains the source of detail.
	cipher, err := d.config.Secrets.Seal("mail:"+id, []byte("You have a JueX notification. Sign in to view it:\n"+d.config.PublicURL+"/t/"+event.Scope.TenantID+"/notifications"))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management.mail_outbox(id,recipient,subject,body_cipher,notification_id) VALUES($1,$2,'JueX notification',$3,$4)`, id, recipient, cipher, notification)
	return err
}

func notificationMailAllowed(ctx context.Context, tx pgx.Tx, id, recipient string) (bool, error) {
	var active, verified, email, completion bool
	var current, kind string
	err := tx.QueryRow(ctx, `SELECT m.status='active',u.email_verified,p.email,p.completions,u.email,n.event->>'kind'
 FROM management.notifications n JOIN management.memberships m ON m.tenant_id=n.tenant_id AND m.user_id=n.user_id
 JOIN management.users u ON u.id=n.user_id JOIN management.notification_preferences p ON p.tenant_id=n.tenant_id AND p.user_id=n.user_id
 WHERE n.id=$1 AND n.visible FOR SHARE OF m,u,p`, id).Scan(&active, &verified, &email, &completion, &current, &kind)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return active && verified && email && current == recipient && (kind != "completed" || completion), err
}
