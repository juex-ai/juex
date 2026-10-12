//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	"github.com/juex-ai/juex/internal/providers"
)

const tenantAuthorityLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('juex.management.tenant:'||$1::uuid::text,0))`

func TestManagementOwnerAuthorityWithoutWrites(t *testing.T) {
	f := managedRuntimeHTTP(t, nil)
	ctx := context.Background()
	config := f.pool.Config().Copy()
	// PostgreSQL rejects tuple-locking reads in this connection policy too.
	// Pure owner checks must not depend on a write transaction and WAL commit.
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	directory := managementpg.NewDirectory(pool, managementpg.Config{})
	for _, execute := range []bool{false, true} {
		name := "read"
		if execute {
			name = "execute"
		}
		t.Run("fleet/"+name, func(t *testing.T) {
			value, err := directory.AuthorizeFleet(ctx, f.actor, f.tenant, f.actor, execute)
			if err != nil || value.Fleet.UserID != f.actor || !value.CanExecute {
				t.Fatal("owner Fleet authority needs a write transaction", value, err)
			}
		})
		t.Run("agent/"+name, func(t *testing.T) {
			value, err := (managed.RuntimeAuthority{Directory: directory}).Authorize(ctx, f.actor, f.tenant, f.agent.ID, execute)
			if err != nil || value.AgentID != f.agent.ID || value.MembershipExecutionEpoch < 1 {
				t.Fatal("owner Agent authority needs a write transaction", value, err)
			}
		})
	}

	t.Run("http through runtime and management RPC", func(t *testing.T) {
		pki := filepath.Join(t.TempDir(), "pki")
		if err := platformrpc.CreateCredentials(pki); err != nil {
			t.Fatal(err)
		}
		listener := platformListener(t)
		server, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(pki, "management"), managed.RuntimeAuthority{Directory: directory})
		if err != nil {
			t.Fatal(err)
		}
		runPlatformRPC(t, server)
		authority, err := runtimeclient.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"), providers.NewProvider)
		if err != nil {
			t.Fatal(err)
		}
		runtimeListener := platformListener(t)
		runtimeServer, err := serverrpc.NewRuntime(runtimeListener, platformrpc.CredentialsAt(pki, "runtime"), &managedruntime.Service{Store: f.store, Authority: authority}, f.pool.Ping)
		if err != nil {
			t.Fatal(err)
		}
		runPlatformRPC(t, runtimeServer)
		client, err := runtimeclient.NewClient(runtimeListener.Addr().String(), platformrpc.CredentialsAt(pki, "management"))
		if err != nil {
			t.Fatal(err)
		}
		auth, err := managementpg.NewAuth(directory)
		if err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewUnstartedServer(nil)
		origin := "http://" + httpServer.Listener.Addr().String()
		handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: directory, Runtime: client, PublicURL: origin, InsecureHTTP: true})
		if err != nil {
			t.Fatal(err)
		}
		httpServer.Config.Handler = handler
		httpServer.Start()
		t.Cleanup(httpServer.Close)
		page := managementCall[managedruntime.Timeline](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+f.agent.ID+"/threads/"+f.main.ID+"/events", origin, nil, 200)
		if page.Thread.ID != f.main.ID || page.Thread.Sequence != f.main.Sequence {
			t.Fatal("owner Timeline lost its committed identity", page)
		}
	})
}

func TestManagementAuthorityWaitsForMembershipCommit(t *testing.T) {
	f := managedRuntimeHTTP(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := f.directory.CreateUser(ctx, "authority-admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, admin.Email, management.Admin, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.AcceptInvitation(ctx, admin.ID, token); err != nil {
		t.Fatal(err)
	}
	// Pause the actual membership mutation inside its transaction, after it has
	// acquired the production authority lock. This works independently of the
	// lock representation and keeps the default repeatable-read test setting.
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION management.pause_membership_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
PERFORM pg_advisory_xact_lock(hashtextextended('test.management.authority:'||current_database(),0)); RETURN NEW; END $$;
CREATE TRIGGER pause_membership_write BEFORE UPDATE ON management.memberships FOR EACH ROW EXECUTE FUNCTION management.pause_membership_write()`); err != nil {
		t.Fatal(err)
	}
	gate, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	if _, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('test.management.authority:'||current_database(),0))`); err != nil {
		t.Fatal(err)
	}
	var gatePID int
	if err := gate.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&gatePID); err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		_, err := f.directory.ChangeMember(ctx, admin.ID, f.tenant, f.actor, management.Admin, management.Suspended)
		changed <- err
	}()
	var writerPID int
	runtimeEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid))`, gatePID).Scan(&writerPID) == nil
	})
	authorized := make(chan error, 1)
	go func() {
		// Different spellings of the same UUID must share the authority fence.
		_, err := f.directory.ReadAgent(ctx, f.actor, strings.ToUpper(f.tenant), f.agent.ID)
		authorized <- err
	}()
	runtimeEventually(t, func() bool {
		var waiting bool
		return f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, writerPID).Scan(&waiting) == nil && waiting
	})
	if err := gate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-changed; err != nil {
		t.Fatal("membership mutation failed", err)
	}
	if err := <-authorized; !errors.Is(err, management.ErrDenied) {
		t.Fatal("waiting owner read retained pre-commit authority", err)
	}
	value, err := f.directory.ReadAgent(ctx, admin.ID, f.tenant, f.agent.ID)
	if err != nil || value.CanExecute {
		t.Fatal("delegated retained read lost its authority boundary", value, err)
	}
	var audits int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE actor_id=$1 AND owner_id=$2 AND agent_id=$3 AND action='agent.read'`, admin.ID, f.actor, f.agent.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("delegated read lost its audit", audits, err)
	}
}
