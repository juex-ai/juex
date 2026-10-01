//go:build postgres

package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

// The supplied database is only used to create and drop isolated test databases.
// An explicitly requested postgres suite fails instead of silently skipping.
func managementDatabase(t *testing.T) (*pgxpool.Pool, *postgres.Directory) {
	t.Helper()
	databaseURL := os.Getenv("JUEX_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Fatal("JUEX_TEST_POSTGRES_URL is required for -tags postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	db := fmt.Sprintf("juex_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+db+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	address, err := url.Parse(databaseURL)
	if err != nil || (address.Scheme != "postgres" && address.Scheme != "postgresql") {
		t.Fatal("JUEX_TEST_POSTGRES_URL must be a PostgreSQL URL")
	}
	address.Path = "/" + db
	query := address.Query()
	query.Del("dbname")
	address.RawQuery = query.Encode()
	// ConnString retains the parsed source, not later Database field changes.
	// CLI fixtures must reconnect to this same disposable database.
	config, err := pgxpool.ParseConfig(address.String())
	if err != nil {
		t.Fatal(err)
	}
	// Business transactions must explicitly choose the isolation level required
	// by their lock protocol, rather than inheriting a deployment default.
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal("repeat migration:", err)
	}
	box, err := secrets.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return pool, postgres.NewDirectory(pool, postgres.Config{Secrets: box, PublicURL: "http://localhost:8680"})
}

func TestManagementTenantIsolationAndRejoin(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	admin, err := d.CreateUser(ctx, "ADMIN@example.com")
	if err != nil {
		t.Fatal(err)
	}
	user, err := d.CreateUser(ctx, "member@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.EmailVerified {
		t.Fatal("creating user proved email ownership")
	}
	if _, err := d.CreateUser(ctx, "Admin@Example.com"); !errors.Is(err, management.ErrConflict) {
		t.Fatalf("duplicate email = %v", err)
	}
	one, err := d.CreateTenant(ctx, "One", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := d.CreateTenant(ctx, "Two", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.Invite(ctx, admin.ID, one.ID, user.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, admin.ID, token); !errors.Is(err, management.ErrInvitation) {
		t.Fatalf("wrong account accepted invitation: %v", err)
	}
	fleet, err := d.AcceptInvitation(ctx, user.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, user.ID, token); !errors.Is(err, management.ErrInvitation) {
		t.Fatalf("replayed single-use invitation: %v", err)
	}
	for _, tc := range []struct {
		actor, tenant, owner string
		allowed              bool
	}{
		{user.ID, one.ID, user.ID, true},
		{admin.ID, one.ID, user.ID, true},
		{user.ID, one.ID, admin.ID, false},
		{admin.ID, two.ID, user.ID, false},
		{user.ID, two.ID, user.ID, true},
	} {
		_, err := d.Fleet(ctx, tc.actor, tc.tenant, tc.owner)
		if (err == nil) != tc.allowed {
			t.Errorf("fleet scope %+v: %v", tc, err)
		}
	}
	if _, err := d.ChangeMember(ctx, user.ID, one.ID, admin.ID, management.Member, management.Active); !errors.Is(err, management.ErrDenied) {
		t.Fatalf("ordinary member changed admin: %v", err)
	}
	if _, err := d.ChangeMember(ctx, admin.ID, one.ID, user.ID, management.Member, management.Suspended); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Fleet(ctx, user.ID, one.ID, user.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("suspended member retains access", err)
	}
	if _, err := d.Fleet(ctx, user.ID, two.ID, user.ID); err != nil {
		t.Fatal("suspension leaked across tenants", err)
	}
	if _, err := d.ChangeMember(ctx, admin.ID, one.ID, user.ID, management.Member, management.Removed); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Fleet(ctx, admin.ID, one.ID, user.ID); err != nil || got.ID != fleet.ID {
		t.Fatal("removed Fleet not retained", got, err)
	}
	if _, err := d.Fleet(ctx, user.ID, one.ID, user.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("removed owner retains access", err)
	}
	if _, err := d.ChangeMember(ctx, admin.ID, one.ID, user.ID, management.Member, management.Active); !errors.Is(err, management.ErrInvalid) {
		t.Fatal("direct rejoin permitted", err)
	}
	_, token, err = d.Invite(ctx, admin.ID, one.ID, user.Email, management.Admin, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := d.AcceptInvitation(ctx, user.ID, token)
	if err != nil || restored != fleet {
		t.Fatal("rejoin replaced Fleet", restored, err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM management.fleets WHERE tenant_id=$1 AND user_id=$2", one.ID, user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	var audits int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM management.audit WHERE tenant_id=$1 AND actor_id=$2 AND owner_id=$3 AND action='membership.changed'", one.ID, admin.ID, user.ID).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("actor/owner audit", audits, err)
	}
}

func TestManagementConcurrentLastAdministrator(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	first, err := d.CreateUser(ctx, "first@example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateUser(ctx, "second@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Concurrent", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.Invite(ctx, first.ID, tenant.ID, second.Email, management.Admin, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, second.ID, token); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := d.ChangeMember(ctx, id, tenant.ID, id, management.Member, management.Active)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, denied := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if errors.Is(err, management.ErrLastAdmin) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || denied != 1 {
		t.Fatalf("succeeded=%d last-admin=%d", succeeded, denied)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM management.memberships WHERE tenant_id=$1 AND role='admin' AND status='active'", tenant.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("lost last administrator", count, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM management.audit WHERE action='membership.changed'").Scan(&count); err != nil || count != 1 {
		t.Fatal("rejected change leaked audit/event", count, err)
	}
}

func TestManagementInvitationExpiryAndConcurrentConsumption(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	admin, err := d.CreateUser(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	user, err := d.CreateUser(ctx, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Invites", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	invitation, expired, err := d.Invite(ctx, admin.ID, tenant.ID, user.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE management.invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", invitation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, user.ID, expired); !errors.Is(err, management.ErrInvitation) {
		t.Fatal("expired invite accepted", err)
	}
	_, token, err := d.Invite(ctx, admin.ID, tenant.ID, user.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, user.ID, expired); !errors.Is(err, management.ErrInvitation) {
		t.Fatal("reissued invite did not revoke old token", err)
	}
	errs := make(chan error, 8)
	for range 8 {
		go func() { _, err := d.AcceptInvitation(ctx, user.ID, token); errs <- err }()
	}
	success := 0
	for range 8 {
		err := <-errs
		if err == nil {
			success++
		} else if !errors.Is(err, management.ErrInvitation) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("invite consumed %d times", success)
	}
	var verified bool
	if err := pool.QueryRow(ctx, "SELECT email_verified FROM management.users WHERE id=$1", user.ID).Scan(&verified); err != nil || verified {
		t.Fatal("invitation proved email", verified, err)
	}
	var issuanceAudits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE invitation_id=$1 AND actor_id=$2 AND owner_id=$3 AND action='invitation.issued'`, invitation.ID, admin.ID, user.ID).Scan(&issuanceAudits); err != nil || issuanceAudits != 2 {
		t.Fatal("invitation issuance lost granting actor", issuanceAudits, err)
	}
}

func TestManagementRollbackIncludesInvitationFleetAndAudit(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	admin, err := d.CreateUser(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	user, err := d.CreateUser(ctx, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Rollback", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	invitation, token, err := d.Invite(ctx, admin.ID, tenant.ID, user.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Fail after membership, Fleet and invitation writes, at the audit boundary.
	if _, err := pool.Exec(ctx, `ALTER TABLE management.audit ADD CONSTRAINT reject_join CHECK (action <> 'membership.joined')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, user.ID, token); err == nil {
		t.Fatal("injected transaction failure not observed")
	}
	var members, fleets, audits int
	var consumed bool
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.memberships WHERE user_id=$1`, user.ID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.fleets WHERE user_id=$1`, user.ID).Scan(&fleets); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM management.invitations WHERE id=$1`, invitation.ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE action='membership.joined'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if members != 0 || fleets != 0 || consumed || audits != 0 {
		t.Fatalf("partial transaction: memberships=%d fleets=%d consumed=%v audits=%d", members, fleets, consumed, audits)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE management.audit DROP CONSTRAINT reject_join`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, user.ID, token); err != nil {
		t.Fatal("rolled-back invitation cannot be consumed", err)
	}
	if _, err := d.CreateTenant(ctx, "Invalid admin", "00000000-0000-0000-0000-000000000000"); !errors.Is(err, management.ErrDenied) {
		t.Fatal(err)
	}
	var tenants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.tenants`).Scan(&tenants); err != nil || tenants != 1 {
		t.Fatal("partial tenant initialization", tenants, err)
	}
}

func TestManagementLastAdminAndRevokedAuthority(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	admin, err := d.CreateUser(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateUser(ctx, "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Authority", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		role   management.Role
		status management.MembershipStatus
	}{
		{management.Member, management.Active},
		{management.Admin, management.Suspended},
		{management.Admin, management.Removed},
	} {
		if _, err := d.ChangeMember(ctx, admin.ID, tenant.ID, admin.ID, change.role, change.status); !errors.Is(err, management.ErrLastAdmin) {
			t.Fatalf("last admin %+v: %v", change, err)
		}
	}
	_, token, err := d.Invite(ctx, admin.ID, tenant.ID, other.Email, management.Admin, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AcceptInvitation(ctx, other.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Invite(ctx, admin.ID, tenant.ID, other.Email, management.Member, time.Hour); !errors.Is(err, management.ErrConflict) {
		t.Fatal("invitation can overwrite current membership", err)
	}
	if _, err := d.ChangeMember(ctx, admin.ID, tenant.ID, other.ID, management.Admin, management.Suspended); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Invite(ctx, other.ID, tenant.ID, "third@example.com", management.Admin, time.Hour); !errors.Is(err, management.ErrDenied) {
		t.Fatal("suspended administrator can grant access", err)
	}
	if _, err := d.ChangeMember(ctx, other.ID, tenant.ID, other.ID, management.Admin, management.Active); !errors.Is(err, management.ErrDenied) {
		t.Fatal("suspended administrator self-restored", err)
	}
	if _, err := d.ChangeMember(ctx, admin.ID, tenant.ID, other.ID, management.Admin, management.Active); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT a.membership_version, a.after_status FROM management.audit a
		WHERE a.owner_id=$1 AND a.action='membership.changed' ORDER BY a.membership_version`, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, want := range []string{"suspended", "active"} {
		if !rows.Next() {
			t.Fatal("lost lifecycle event")
		}
		var version int
		var status string
		if err := rows.Scan(&version, &status); err != nil {
			t.Fatal(err)
		}
		if version != i+2 || status != want {
			t.Fatalf("event = %d %s", version, status)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("invalid event stream", rows.Err())
	}
}

func TestManagementMigrationIntegrity(t *testing.T) {
	pool, _ := managementDatabase(t)
	ctx := context.Background()
	errs := make(chan error, 6)
	for range 6 {
		go func() { errs <- postgres.Migrate(ctx, pool) }()
	}
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatal("concurrent migration", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE management.schema_versions SET checksum='changed' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err == nil {
		t.Fatal("modified migration accepted")
	}
	var checksum string
	if err := pool.QueryRow(ctx, `SELECT checksum FROM management.schema_versions WHERE version=1`).Scan(&checksum); err != nil || checksum != "changed" {
		t.Fatal("silently repaired schema", checksum, err)
	}
}
