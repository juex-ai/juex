//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	"github.com/juex-ai/juex/internal/providers"
)

func TestPlatformRPCDeadlineReleasesAuthorizationLockWait(t *testing.T) {
	f := managedRuntimeHTTP(t, nil)
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(directory); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	service, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(directory, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, service)
	authority, err := runtimeclient.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(directory, "runtime"), providers.NewProvider)
	if err != nil {
		t.Fatal(err)
	}
	runtimeListener := platformListener(t)
	runtimeService := &managedruntime.Service{Store: f.store, Authority: authority}
	runtimeServer, err := serverrpc.NewRuntime(runtimeListener, platformrpc.CredentialsAt(directory, "runtime"), runtimeService, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, runtimeServer)
	client, err := runtimeclient.NewClient(runtimeListener.Addr().String(), platformrpc.CredentialsAt(directory, "management"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Events(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, 0, 50); err != nil {
		t.Fatal(err)
	}

	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Rollback(ctx) })
	if _, err := blocker.Exec(ctx, `SELECT id FROM management.tenants WHERE id=$1 FOR UPDATE`, f.tenant); err != nil {
		t.Fatal(err)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.Events(deadlineCtx, f.actor, f.tenant, f.agent.ID, f.main.ID, 0, 50)
		result <- err
	}()
	var waitingPID int
	runtimeEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query='SELECT id FROM management.tenants WHERE id=$1 FOR UPDATE' LIMIT 1`).Scan(&waitingPID) == nil
	})
	select {
	case err := <-result:
		if !errors.Is(err, platformrpc.ErrUnavailable) {
			t.Fatalf("expired authorization = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RPC client ignored deadline")
	}
	// Keep the blocker locked: releasing it would hide an abandoned handler.
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		var active bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state <> 'idle')`, waitingPID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if !active {
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Events(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, 0, 50); err != nil {
				t.Fatal("client did not recover", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expired RPC still occupies a database connection waiting for tenant authority")
}

type unavailableRuntimeAuthority struct{ managedruntime.Authority }

func (unavailableRuntimeAuthority) Authorize(context.Context, string, string, string, bool) (managedruntime.Scope, error) {
	return managedruntime.Scope{}, platformrpc.ErrUnavailable
}

func TestPlatformRPCUnavailableReachesHTTPAsServiceUnavailable(t *testing.T) {
	f := managedRuntimeHTTP(t, nil)
	directory := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(directory); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	service := &managedruntime.Service{Store: f.store, Authority: unavailableRuntimeAuthority{f.authority}}
	runtimeServer, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(directory, "runtime"), service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, runtimeServer)
	client, err := runtimeclient.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(directory, "management"))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Runtime: client, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	response := managementCall[map[string]string](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+f.agent.ID+"/threads/"+f.main.ID+"/events", origin, nil, 503)
	if response["code"] != "service_unavailable" || response["error"] != platformrpc.ErrUnavailable.Error() {
		t.Fatal(response)
	}
}
