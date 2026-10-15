//go:build postgres

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagedEnvironmentHTTPReadsPreserveDurableState(t *testing.T) {
	f, backend, environment := hostedFixture(t)
	ctx := context.Background()
	listener := platformListener(t)
	service, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, service)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Execution: client, PublicURL: f.origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewServer(handler)
	defer dashboard.Close()
	bounded := *f.client
	bounded.Timeout = 2 * time.Second
	list := func(t *testing.T) {
		t.Helper()
		environments := managementCall[[]execprotocol.Environment](t, &bounded, http.MethodGet, dashboard.URL+"/api/tenants/"+f.tenant+"/agents/"+f.agent.ID+"/environments", f.origin, nil, http.StatusOK)
		if len(environments) != 1 || environments[0].ID != environment.ID || !environments[0].Default || len(environments[0].Capabilities) == 0 {
			t.Fatal("listing lost the owned default environment", environments)
		}
	}
	t.Run("unchanged authority does not rewrite the environment", func(t *testing.T) {
		read := func() [2]string {
			t.Helper()
			var version [2]string
			if err := f.pool.QueryRow(ctx, `SELECT xmin::text,ctid::text FROM execution.environments WHERE id=$1`, environment.ID).Scan(&version[0], &version[1]); err != nil {
				t.Fatal(err)
			}
			return version
		}
		before := read()
		for range 2 {
			list(t)
		}
		if after := read(); after != before {
			t.Fatal("unchanged authority rewrote its durable row", before, after)
		}
	})
	t.Run("listing does not wait for an unrelated device update", func(t *testing.T) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `UPDATE execution.environments SET online_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, environment.ID); err != nil {
			t.Fatal(err)
		}
		list(t)
	})
	if backend.starts != 0 {
		t.Fatal("listing started an idle executor")
	}
}
