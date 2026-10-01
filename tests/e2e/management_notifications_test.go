//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/maildelivery"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagementNotificationOwnerDedupAndPreferences(t *testing.T) {
	_, d := managementDatabase(t)
	ctx := context.Background()
	admin, _ := d.CreateUser(ctx, "admin@example.test")
	owner, _ := d.CreateUser(ctx, "owner@example.test")
	tenant, err := d.CreateTenant(ctx, "Inbox", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.Invite(ctx, admin.ID, tenant.ID, owner.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := d.AcceptInvitation(ctx, owner.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	event := application.Event{ID: uuid.NewString(), Application: "memory", ResourceID: "review", Kind: "attention", Title: "Memory review failed", Scope: application.Scope{Access: application.Access{ActorID: admin.ID, TenantID: tenant.ID, UserID: owner.ID}, FleetID: fleet.ID, ActorEpoch: 1, MemberEpoch: 1}, Epoch: 1, CreatedAt: time.Now()}
	for i := 0; i < 2; i++ {
		if err := d.RecordNotification(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.Notifications(ctx, owner.ID, tenant.ID, 0, 20)
	if err != nil || len(page.Items) != 1 || page.Unread != 1 {
		t.Fatal(page, err)
	}
	adminPage, err := d.Notifications(ctx, admin.ID, tenant.ID, 0, 20)
	if err != nil || len(adminPage.Items) != 0 {
		t.Fatal("delegated actor received owner's notification", adminPage, err)
	}
	if err := d.MarkNotification(ctx, admin.ID, tenant.ID, page.Items[0].ID, true); !errors.Is(err, management.ErrDenied) {
		t.Fatal("admin altered owner's private read state", err)
	}
	if err := d.MarkNotification(ctx, owner.ID, tenant.ID, page.Items[0].ID, true); err != nil {
		t.Fatal(err)
	}
	event.Title = "different payload"
	if err := d.RecordNotification(ctx, event); !errors.Is(err, management.ErrConflict) {
		t.Fatal("event ID reused", err)
	}
	event.ID = uuid.NewString()
	event.Kind = "completed"
	if err := d.RecordNotification(ctx, event); err != nil {
		t.Fatal(err)
	}
	page, err = d.Notifications(ctx, owner.ID, tenant.ID, 0, 20)
	if err != nil || len(page.Items) != 1 || page.Unread != 0 {
		t.Fatal("completion ignored default preference", page, err)
	}
	pref, err := d.NotificationPreferences(ctx, owner.ID, tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	pref.Email = true
	if _, err := d.ConfigureNotifications(ctx, owner.ID, tenant.ID, pref); !errors.Is(err, management.ErrInvalid) {
		t.Fatal("enabled mail without SMTP/verified email", err)
	}
	pref.Email = false
	pref.Completions = true
	if _, err := d.ConfigureNotifications(ctx, owner.ID, tenant.ID, pref); err != nil {
		t.Fatal(err)
	}
	event.ID = uuid.NewString()
	if err := d.RecordNotification(ctx, event); err != nil {
		t.Fatal(err)
	}
	page, err = d.Notifications(ctx, owner.ID, tenant.ID, 0, 20)
	if err != nil || len(page.Items) != 2 || page.Unread != 1 {
		t.Fatal(page, err)
	}
}

func TestManagementNotificationHTTPPersistenceAndIsolation(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("notification request invoked model") })
	ctx := context.Background()
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, UserID: f.actor, TenantID: f.tenant, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	event := application.Event{ID: uuid.NewString(), Application: "runtime", ResourceID: f.main.ID, Kind: "attention", Title: "需要确认操作结果", Scope: scope, Epoch: 1, CreatedAt: time.Now()}
	if err := f.directory.RecordNotification(ctx, event); err != nil {
		t.Fatal(err)
	}
	base := f.origin + "/api/tenants/" + f.tenant
	page := managementCall[management.NotificationPage](t, f.client, "GET", base+"/notifications", f.origin, nil, 200)
	if len(page.Items) != 1 || page.Unread != 1 {
		t.Fatal(page)
	}
	managementCall[any](t, f.client, "PUT", base+"/notifications/"+page.Items[0].ID, f.origin, map[string]bool{"read": true}, 200)
	page = managementCall[management.NotificationPage](t, f.client, "GET", base+"/notifications", f.origin, nil, 200)
	if page.Unread != 0 || !page.Items[0].Read {
		t.Fatal(page)
	}
	managementCall[any](t, managementClient(t), "GET", base+"/notifications", f.origin, nil, 401)
	managementCall[any](t, f.client, "GET", f.origin+"/api/tenants/"+uuid.NewString()+"/notifications", f.origin, nil, 403)
	managementCall[any](t, f.client, "GET", base+"/notifications?before=-1", f.origin, nil, 400)
	pref := managementCall[management.NotificationPreferences](t, f.client, "GET", base+"/notification-preferences", f.origin, nil, 200)
	pref.Completions = true
	managementCall[management.NotificationPreferences](t, f.client, "PUT", base+"/notification-preferences", f.origin, pref, 200)
	managementCall[any](t, f.client, "PUT", base+"/notification-preferences", f.origin, pref, 409)
}

func TestManagementNotificationMailRechecksCurrentRecipientAuthority(t *testing.T) {
	for _, change := range []string{"none", "unsubscribe", "unverified", "address", "suspended"} {
		t.Run(change, func(t *testing.T) {
			pool, _ := managementDatabase(t)
			ctx := context.Background()
			box, err := secrets.New([]byte(strings.Repeat("k", 32)))
			if err != nil {
				t.Fatal(err)
			}
			d := managementpg.NewDirectory(pool, managementpg.Config{Secrets: box, PublicURL: "https://juex.example.test", MailEnabled: true})
			user, err := d.CreateUser(ctx, "notify@example.test")
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := d.CreateTenant(ctx, "Notices", user.ID)
			if err != nil {
				t.Fatal(err)
			}
			fleet, err := d.Fleet(ctx, user.ID, tenant.ID, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE management.users SET email_verified=true WHERE id=$1`, user.ID); err != nil {
				t.Fatal(err)
			}
			pref, err := d.NotificationPreferences(ctx, user.ID, tenant.ID)
			if err != nil {
				t.Fatal(err)
			}
			pref.Email = true
			pref, err = d.ConfigureNotifications(ctx, user.ID, tenant.ID, pref)
			if err != nil {
				t.Fatal(err)
			}
			event := application.Event{ID: uuid.NewString(), Application: "calendar", ResourceID: "occurrence", Kind: "reminder", Title: "private title", Summary: "private evidence", Epoch: 1, CreatedAt: time.Now(), Scope: application.Scope{Access: application.Access{ActorID: user.ID, UserID: user.ID, TenantID: tenant.ID}, FleetID: fleet.ID, ActorEpoch: 1, MemberEpoch: 1}}
			if err := d.RecordNotification(ctx, event); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "unsubscribe":
				pref.Email = false
				_, err = d.ConfigureNotifications(ctx, user.ID, tenant.ID, pref)
			case "unverified":
				_, err = pool.Exec(ctx, `UPDATE management.users SET email_verified=false WHERE id=$1`, user.ID)
			case "address":
				_, err = pool.Exec(ctx, `UPDATE management.users SET email='new@example.test' WHERE id=$1`, user.ID)
			case "suspended":
				_, err = pool.Exec(ctx, `UPDATE management.memberships SET status='suspended' WHERE user_id=$1`, user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			worked, err := d.DeliverMail(ctx, managementMailSender(func(_ context.Context, m maildelivery.Message) error {
				calls++
				if m.To != user.Email || strings.Contains(m.Body, "private") || !strings.Contains(m.Body, "/notifications") {
					t.Fatal("external mail leaked application content", m)
				}
				return nil
			}))
			if err != nil || !worked || (calls == 1) != (change == "none") {
				t.Fatal(change, calls, worked, err)
			}
			worked, err = d.DeliverMail(ctx, managementMailSender(func(context.Context, maildelivery.Message) error { t.Fatal("delivery replayed"); return nil }))
			if err != nil || worked {
				t.Fatal(worked, err)
			}
		})
	}
}
