//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/providers"
)

func TestPlatformFixedEndpointsWithoutDNS(t *testing.T) {
	f := managedRuntimeHTTP(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database := f.pool.Config().ConnConfig.Copy()
	addresses, err := net.DefaultResolver.LookupHost(ctx, database.Host)
	if err != nil || len(addresses) == 0 {
		t.Fatal("test database address", err)
	}
	database.Host = addresses[0]
	database.Fallbacks = nil
	var lookups atomic.Int64
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		lookups.Add(1)
		return nil, errors.New("test DNS unavailable")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	database.LookupFunc = net.DefaultResolver.LookupHost
	// Both connections are constructed after DNS fails; no warm connection can
	// hide a name-resolution dependency in the transport under test.
	if _, err := net.DefaultResolver.LookupHost(ctx, "unavailable.juex.test"); err == nil || lookups.Load() == 0 {
		t.Fatal("DNS failure was not exercised", err)
	}
	before := lookups.Load()
	connection, err := pgx.ConnectConfig(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	var agent string
	if err := connection.QueryRow(ctx, "SELECT id::text FROM management.agents WHERE id=$1", f.agent.ID).Scan(&agent); err != nil || agent != f.agent.ID {
		t.Fatal("fresh database connection", agent, err)
	}
	directory := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(directory); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"management", "memory"} {
		t.Run(identity, func(t *testing.T) {
			// Reserve the exact test dial address. On Darwin a wildcard may
			// share its port with an unrelated, more-specific HTTP listener.
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			service, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(directory, identity), f.authority)
			if err != nil {
				t.Fatal(err)
			}
			runPlatformRPC(t, service)
			authority, err := runtimeclient.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(directory, "runtime"), providers.NewProvider)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, false)
			if identity != "management" {
				if !errors.Is(err, platformrpc.ErrUnavailable) {
					t.Fatal("fixed address admitted wrong server identity", err)
				}
				return
			}
			if err != nil || scope.AgentID != f.agent.ID {
				t.Fatal("fresh authority RPC", scope.AgentID, err)
			}
			if _, err := authority.Authorize(ctx, "00000000-0000-4000-8000-000000000001", f.tenant, f.agent.ID, false); !errors.Is(err, managedruntime.ErrDenied) {
				t.Fatal("fixed address bypassed Agent authority", err)
			}
		})
	}
	if lookups.Load() != before {
		t.Fatal("fixed endpoint still depended on DNS")
	}
}
