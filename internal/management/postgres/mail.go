package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/maildelivery"
)

type MailSender interface {
	Send(context.Context, maildelivery.Message) error
}

// DeliverMail sends at most one durable message. SMTP has no exactly-once
// acknowledgement: a crash after remote acceptance can redeliver the same ID.
func (d *Directory) DeliverMail(ctx context.Context, sender MailSender) (bool, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var message maildelivery.Message
	var cipher []byte
	var attempts int
	var notification string
	err = tx.QueryRow(ctx, `SELECT id,recipient,subject,body_cipher,attempts,COALESCE(notification_id::text,'') FROM management.mail_outbox
	WHERE delivered_at IS NULL AND attempts<8 AND next_attempt_at<=clock_timestamp()
	ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&message.ID, &message.To, &message.Subject, &cipher, &attempts, &notification)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if notification != "" {
		allowed, err := notificationMailAllowed(ctx, tx, notification, message.To)
		if err != nil {
			return false, err
		}
		if !allowed || !d.config.MailEnabled {
			if _, err := tx.Exec(ctx, `UPDATE management.mail_outbox SET delivered_at=clock_timestamp(),body_cipher=''::bytea,last_error='notification_suppressed' WHERE id=$1`, message.ID); err != nil {
				return false, err
			}
			return true, tx.Commit(ctx)
		}
	}
	body, err := d.config.Secrets.Open("mail:"+message.ID, cipher)
	if err != nil {
		return false, err
	}
	message.Body = string(body)
	if err := sender.Send(ctx, message); err != nil {
		// Transport errors can include server-controlled text. Persist only a
		// classification, never SMTP credentials or capability-bearing content.
		_, err = tx.Exec(ctx, `UPDATE management.mail_outbox SET attempts=attempts+1,last_error='delivery_failed',next_attempt_at=clock_timestamp()+make_interval(secs=>$2) WHERE id=$1`, message.ID, int((time.Minute * time.Duration(1<<attempts)).Seconds()))
		if err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE management.mail_outbox SET delivered_at=clock_timestamp(),attempts=attempts+1,last_error='',body_cipher=''::bytea WHERE id=$1`, message.ID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
