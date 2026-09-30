//go:build postgres

package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/maildelivery"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

type managementMailSender func(context.Context, maildelivery.Message) error

func (f managementMailSender) Send(ctx context.Context, message maildelivery.Message) error {
	return f(ctx, message)
}

func TestManagementDurableMailDelivery(t *testing.T) {
	pool, _ := managementDatabase(t)
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	d := postgres.NewDirectory(pool, postgres.Config{Secrets: box, PublicURL: "https://juex.example.test", MailEnabled: true})
	ctx := context.Background()
	admin, err := d.CreateUser(ctx, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Mail", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.Invite(ctx, admin.ID, tenant.ID, "member@example.test", management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	status := func(want string) {
		t.Helper()
		invitations, err := d.Invitations(ctx, admin.ID, tenant.ID)
		if err != nil || len(invitations) != 1 || invitations[0].DeliveryStatus != want {
			t.Fatalf("status %s: %+v %v", want, invitations, err)
		}
	}
	status("queued")
	var firstID string
	worked, err := d.DeliverMail(ctx, managementMailSender(func(_ context.Context, message maildelivery.Message) error {
		firstID = message.ID
		if !strings.Contains(message.Body, token) {
			t.Fatal("queued email lost invitation")
		}
		return errors.New("SMTP failure with secret-looking content")
	}))
	if !worked || err != nil {
		t.Fatal(worked, err)
	}
	status("retrying")
	if worked, err := d.DeliverMail(ctx, managementMailSender(func(context.Context, maildelivery.Message) error { t.Fatal("retried without backoff"); return nil })); worked || err != nil {
		t.Fatal(worked, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE management.mail_outbox SET next_attempt_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	worked, err = d.DeliverMail(ctx, managementMailSender(func(_ context.Context, message maildelivery.Message) error {
		if message.ID != firstID {
			t.Fatal("retry changed durable message ID")
		}
		return nil
	}))
	if !worked || err != nil {
		t.Fatal(worked, err)
	}
	status("sent")
	var bytes int
	if err := pool.QueryRow(ctx, `SELECT octet_length(body_cipher) FROM management.mail_outbox WHERE id=$1`, firstID).Scan(&bytes); err != nil || bytes != 0 {
		t.Fatal("delivered capability still in outbox", bytes, err)
	}
	if worked, err := d.DeliverMail(ctx, managementMailSender(func(context.Context, maildelivery.Message) error { t.Fatal("resent confirmed delivery"); return nil })); worked || err != nil {
		t.Fatal(worked, err)
	}
}

func TestManagementOperatorTenantProvisioning(t *testing.T) {
	_, d := managementDatabase(t)
	auth, err := postgres.NewAuth(d)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := auth.ProvisionTenant(ctx, "One", "admin@example.test")
	if err != nil || first.SetupURL == "" {
		t.Fatal(first, err)
	}
	if err := auth.SetPassword(ctx, linkToken(t, first.SetupURL), "original admin password"); err != nil {
		t.Fatal(err)
	}
	second, err := auth.ProvisionTenant(ctx, "Two", "admin@example.test")
	if err != nil || second.SetupURL != "" {
		t.Fatal("existing account received password reset capability", second, err)
	}
	session, err := auth.Login(ctx, "admin@example.test", "original admin password")
	if err != nil {
		t.Fatal(err)
	}
	tenants, err := d.Tenants(ctx, session.User.ID)
	if err != nil || len(tenants) != 2 || tenants[0].FleetID == tenants[1].FleetID {
		t.Fatal(tenants, err)
	}
	if _, err := auth.BeginBootstrap(ctx, "Third", "hijack@example.test"); !errors.Is(err, management.ErrConflict) {
		t.Fatal(err)
	}
	third, err := auth.ProvisionTenant(ctx, "Three", "other@example.test")
	if err != nil || third.SetupURL == "" {
		t.Fatal(third, err)
	}
	if _, err := d.Fleet(ctx, session.User.ID, third.Tenant.ID, session.User.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("cross-tenant access", err)
	}
}
