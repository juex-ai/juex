//go:build postgres

package e2e

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
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
		origin := managementAuthorityHTTP(t, f, directory)
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

func managementAuthorityHTTP(t *testing.T, f *managedRuntimeFixture, directory *managementpg.Directory) string {
	t.Helper()
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
	return origin
}

type modelResolutionPauseKey struct{}

type modelResolutionPause struct {
	paused  chan uint32
	release chan struct{}
	once    sync.Once
}

func (p *modelResolutionPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT m.id,m.provider") && strings.Contains(data.SQL, "m.key_cipher,m.options_cipher") {
		return context.WithValue(ctx, modelResolutionPauseKey{}, true)
	}
	return ctx
}

func (p *modelResolutionPause) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(modelResolutionPauseKey{}) != true || data.Err != nil {
		return
	}
	p.once.Do(func() {
		p.paused <- conn.PgConn().PID()
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	})
}

func TestManagementModelResolutionDoesNotSerializeAuthorityReaders(t *testing.T) {
	for _, mutation := range []string{"membership", "configuration", "agent management"} {
		t.Run(mutation, func(t *testing.T) { managementModelAuthorityFence(t, mutation) })
	}
}

func managementModelAuthorityFence(t *testing.T, mutation string) {
	t.Helper()
	f := managedRuntimeHTTP(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := f.directory.CreateUser(ctx, "concurrent-authority@example.test")
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
	f.agent, err = f.directory.SetAgentManagement(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := modelCallScope(authority)
	plan, err := f.directory.SnapshotPlan(ctx, scope)
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatal(plan, err)
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Independent reader"})
	if err != nil {
		t.Fatal(err)
	}
	otherThreads := managementCall[[]managedruntime.Thread](t, f.client, "GET", f.origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/threads", f.origin, nil, 200)
	if len(otherThreads) != 1 {
		t.Fatal(otherThreads)
	}
	origin := managementAuthorityHTTP(t, f, f.directory)
	config := f.pool.Config().Copy()
	pause := &modelResolutionPause{paused: make(chan uint32, 1), release: make(chan struct{})}
	config.ConnConfig.Tracer = pause
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	box, err := secrets.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	directory := managementpg.NewDirectory(pool, managementpg.Config{Secrets: box})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(pause.release) }) }
	defer release()
	resolved := make(chan error, 1)
	go func() {
		connection, err := directory.ResolveCandidate(ctx, scope, plan.Candidates[0])
		if err == nil && connection.APIKey != "test-key" {
			err = errors.New("model resolution lost its original credential")
		}
		resolved <- err
	}()
	var readerPID uint32
	select {
	case readerPID = <-pause.paused:
	case err := <-resolved:
		t.Fatal("model read did not reach transaction pause", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if mutation == "membership" {
		// Stop the real ResolveCandidate after its model SELECT, before Commit.
		// A slow reader must not serialize other readers, including another Agent's
		// public Timeline through both authenticated RPC boundaries.
		t.Run("HTTP Timeline for another Agent", func(t *testing.T) {
			client := *f.client
			client.Timeout = time.Second
			page := managementCall[managedruntime.Timeline](t, &client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/threads/"+otherThreads[0].ID+"/events", origin, nil, 200)
			if page.Thread.ID != otherThreads[0].ID || page.Thread.AgentID != other.ID {
				t.Fatal(page)
			}
		})
		checks := map[string]func(context.Context) error{
			"Agent read": func(ctx context.Context) error {
				_, err := f.directory.ReadAgent(ctx, f.actor, strings.ToUpper(f.tenant), f.agent.ID)
				return err
			},
			"Agent execute": func(ctx context.Context) error {
				_, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
				return err
			},
			"Fleet read": func(ctx context.Context) error {
				_, err := f.directory.AuthorizeFleet(ctx, f.actor, f.tenant, f.actor, false)
				return err
			},
			"Fleet execute": func(ctx context.Context) error {
				_, err := f.directory.AuthorizeFleet(ctx, f.actor, f.tenant, f.actor, true)
				return err
			},
			"model plan": func(ctx context.Context) error { _, err := f.directory.SnapshotPlan(ctx, scope); return err },
			"model admission": func(ctx context.Context) error {
				_, err := f.directory.ResolveCandidate(ctx, scope, plan.Candidates[0])
				return err
			},
		}
		for name, check := range checks {
			t.Run(name, func(t *testing.T) {
				readCtx, readCancel := context.WithTimeout(ctx, time.Second)
				defer readCancel()
				if err := check(readCtx); err != nil {
					t.Fatal("independent authority reader waited for model reader", err)
				}
			})
		}
	}
	changed := make(chan error, 1)
	go func() {
		var err error
		switch mutation {
		case "membership":
			_, err = f.directory.ChangeMember(ctx, admin.ID, f.tenant, f.actor, management.Admin, management.Suspended)
		case "configuration":
			config := f.agent.Configuration.Clone()
			config.Modules = map[agentpolicy.Capability]bool{"mcp": false}
			_, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &config})
		case "agent management":
			_, err = f.directory.SetAgentManagement(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, false)
		}
		changed <- err
	}()
	var writerPID int
	runtimeEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid))`, readerPID).Scan(&writerPID) == nil
	})
	afterWrite := make(chan error, 1)
	go func() {
		_, err := f.directory.ResolveCandidate(ctx, scope, plan.Candidates[0])
		afterWrite <- err
	}()
	runtimeEventually(t, func() bool {
		var waiting bool
		return f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, writerPID).Scan(&waiting) == nil && waiting
	})
	release()
	if err := <-resolved; err != nil {
		t.Fatal("admitted model read failed", err)
	}
	if err := <-changed; err != nil {
		t.Fatal("authority writer failed", err)
	}
	if err := <-afterWrite; !errors.Is(err, management.ErrDenied) {
		t.Fatal("model admission retained authority after queued revocation", err)
	}
}
