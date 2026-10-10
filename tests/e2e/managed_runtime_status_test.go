//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func statusRows(t *testing.T, pool *pgxpool.Pool, tables ...string) []string {
	t.Helper()
	values := make([]string, len(tables))
	for index, table := range tables {
		if err := pool.QueryRow(context.Background(), `SELECT md5(COALESCE(string_agg(v::text,E'\n' ORDER BY v::text),'')) FROM (SELECT to_jsonb(t) v FROM `+table+` t) rows`).Scan(&values[index]); err != nil {
			t.Fatal(err)
		}
	}
	return values
}

func TestManagedRuntimeStatusHTTPRPCDoesNotInitializeOrMutate(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(f.credentials, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(nil)
	origin := "http://" + web.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Runtime: client, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	web.Config.Handler = handler
	web.Start()
	t.Cleanup(web.Close)
	managementCall[management.Session](t, f.client, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "runtime@example.test", "password": "runtime test password"}, 200)
	read := func(agent string, code int) managedruntime.RuntimeStatus {
		return managementCall[managedruntime.RuntimeStatus](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+agent+"/runtime-status", origin, nil, code)
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Not started"})
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"runtime.agents", "runtime.threads", "runtime.inputs", "runtime.events"}
	before := statusRows(t, f.pool, tables...)
	for range 3 {
		view := read(other.ID, 200)
		if view.Initialized || view.MainThreadID != "" || view.ActiveThreads != 0 || view.LastActivity != nil || view.ObservedAt.IsZero() {
			t.Fatal(view)
		}
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("read initialized Runtime")
	}
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "archived-worker", "History")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Archive(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, true); err != nil {
		t.Fatal(err)
	}
	input := f.submit(t, "status-pending", f.main.ID, "Queue without running provider")
	before = statusRows(t, f.pool, tables...)
	view := read(f.agent.ID, 200)
	if !view.Initialized || view.MainThreadID != f.main.ID || view.ActiveThreads != 1 || view.ArchivedThreads != 1 || view.PendingInputs != 1 || view.LastActivity == nil || view.States["queued"] != 1 {
		t.Fatal(view)
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("status changed work")
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, f.agent.ID, "status-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.BeginTurn(ctx, lease, scope, input.ID, config); err != nil {
		t.Fatal(err)
	}
	if view := read(f.agent.ID, 200); view.States["running"] != 1 || view.States["unconfirmed"] != 0 {
		t.Fatal("active turn not visible", view)
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	before = statusRows(t, f.pool, tables...)
	if view := read(f.agent.ID, 200); view.States["running"] != 0 || view.States["unconfirmed"] != 1 {
		t.Fatal("released activation still processing", view)
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("status recovered work")
	}
	if _, err := f.store.Claim(ctx, f.agent.ID, "replacement", time.Minute); err != nil {
		t.Fatal(err)
	}
	if view := read(f.agent.ID, 200); view.States["unconfirmed"] != 1 {
		t.Fatal("new holder revived old epoch", view)
	}
	if _, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true); err != nil {
		t.Fatal(err)
	}
	read(f.agent.ID, 200)
	outsider, err := f.directory.CreateUser(ctx, "status-outsider@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(ctx, outsider.ID, f.tenant, f.agent.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-owner status", err)
	}
}

func TestManagedEnvironmentStatusDoesNotProvisionWakeOrRenew(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	// Any call to Ensure would fail on this deliberately incomplete manager.
	f.execution.Managed = &execution.ManagedManager{}
	_, origin := extensionManagement(t, f)
	url := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/environment-status"
	read := func() execution.EnvironmentInspection {
		return managementCall[execution.EnvironmentInspection](t, f.client, "GET", url, origin, nil, 200)
	}
	tables := []string{"execution.environments", "execution.managed_environments", "execution.default_environments", "execution.operations", "execution.audit"}
	before := statusRows(t, f.pool, tables...)
	for range 3 {
		view := read()
		if view.DefaultState != "unprovisioned" || len(view.Environments) != 0 || view.ObservedAt.IsZero() {
			t.Fatal(view)
		}
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("read provisioned environment")
	}
	device, _ := f.pairDevice(t)
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: "/Users/test/selected"}); err != nil {
		t.Fatal(err)
	}
	before = statusRows(t, f.pool, tables...)
	view := read()
	if view.DefaultState != "selected" || len(view.Environments) != 1 || !view.Environments[0].Default || view.Environments[0].Online || view.Environments[0].WorkingDirectory != "/Users/test/selected" || view.Environments[0].JournalID != "" {
		t.Fatal(view)
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("read renewed device or mutated default")
	}
	if _, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true); err != nil {
		t.Fatal(err)
	}
	read()
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, device.ID); err != nil {
		t.Fatal(err)
	}
	view = read()
	if view.DefaultState != "unavailable" || view.Binding.EnvironmentID != device.ID || len(view.Environments) != 0 {
		t.Fatal("revoked default silently replaced", view)
	}
	outsider, err := f.directory.CreateUser(ctx, "environment-outsider@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.InspectEnvironments(ctx, outsider.ID, f.tenant, f.agent.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("cross-owner environment status", err)
	}
}
