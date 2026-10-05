//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/kitex/server"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/providers"
)

func runPlatformRPC(t *testing.T, service server.Server) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- service.Run() }()
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Kitex server did not stop")
		}
	})
}
func platformListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func TestManagedRuntimeKitexMutualTLSAndConversation(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		streamManagedReply(w, "Reply over independent RPC services")
	})
	directory := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(directory); err != nil {
		t.Fatal(err)
	}
	if err := platformrpc.CreateCredentials(directory); err == nil {
		t.Fatal("existing platform identity replaced")
	}
	managementListener := platformListener(t)
	managementServer, err := serverrpc.NewManagement(managementListener, platformrpc.CredentialsAt(directory, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, managementServer)
	authority, err := runtimeclient.NewAuthority(managementListener.Addr().String(), platformrpc.CredentialsAt(directory, "runtime"), providers.NewProvider)
	if err != nil {
		t.Fatal(err)
	}
	runtimeListener := platformListener(t)
	service := &managedruntime.Service{Store: f.store, Authority: authority}
	runtimeServer, err := serverrpc.NewRuntime(runtimeListener, platformrpc.CredentialsAt(directory, "runtime"), service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, runtimeServer)
	client, err := runtimeclient.NewClient(runtimeListener.Addr().String(), platformrpc.CredentialsAt(directory, "management"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	executorAuthority, err := runtimeclient.NewAuthority(managementListener.Addr().String(), platformrpc.CredentialsAt(directory, "execution"), providers.NewProvider)
	if err != nil {
		t.Fatal(err)
	}
	executorScope, err := executorAuthority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal("Execution could not check Agent authority", err)
	}
	if _, err := executorAuthority.Snapshot(ctx, executorScope); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Execution obtained Runtime-only configuration", err)
	}
	if _, err := executorAuthority.Peers(ctx, executorScope); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Execution obtained Runtime peer directory", err)
	}
	if peers, err := authority.Peers(ctx, executorScope); err != nil || len(peers) != 0 {
		t.Fatal(peers, err)
	}
	if _, err := executorAuthority.Provider(ctx, executorScope, managedruntime.ModelConfig{}, managedruntime.ModelRequirements{}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Execution obtained model credentials", err)
	}
	threads, err := client.Threads(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil || len(threads) != 1 {
		t.Fatal(threads, err)
	}
	input := managedruntime.InputRequest{RequestID: "rpc-input", ThreadID: threads[0].ID, Text: "Hello through RPC"}
	first, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, input)
	if err != nil || first.ID != second.ID {
		t.Fatal("RPC duplicate admission", err)
	}
	worker, err := client.Worker(ctx, f.actor, f.tenant, f.agent.ID, threads[0].ID, "rpc-worker", "Research")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Compact(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, managedruntime.CompactionRequest{RequestID: "rpc-compact"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Cancel(ctx, f.actor, f.tenant, f.agent.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	if archived, err := client.Archive(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, true); err != nil || archived.Retention != "archived" {
		t.Fatal(archived, err)
	}
	if restored, err := client.Archive(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, false); err != nil || restored.Retention != "active" {
		t.Fatal(restored, err)
	}
	if _, err := client.Threads(ctx, "00000000-0000-4000-8000-000000000001", f.tenant, f.agent.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("RPC authority missing", err)
	}
	runner, err := managedruntime.NewRunner(f.store, authority, managedruntime.RunnerConfig{PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { runner.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	runtimeEventually(t, func() bool {
		timeline, err := client.Events(ctx, f.actor, f.tenant, f.agent.ID, threads[0].ID, 0, 200)
		return err == nil && timeline.Thread.State == "idle" && timeline.Thread.PendingInputs == 0
	})
	wrongRole, err := runtimeclient.NewClient(runtimeListener.Addr().String(), platformrpc.CredentialsAt(directory, "execution"))
	usage, usageErr := client.Usage(ctx, f.actor, usageQuery(f.tenant, f.actor))
	if usageErr != nil || usage.Totals.Attempts != 1 || usage.Totals.TotalTokens != 16 {
		t.Fatal("usage did not cross Runtime and Management authority RPCs", usage, usageErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongRole.Health(ctx); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal("wrong service role admitted", err)
	}
	otherCA := filepath.Join(t.TempDir(), "other-pki")
	if err := platformrpc.CreateCredentials(otherCA); err != nil {
		t.Fatal(err)
	}
	untrusted, err := runtimeclient.NewClient(runtimeListener.Addr().String(), platformrpc.CredentialsAt(otherCA, "management"))
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusted.Health(ctx); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal("untrusted certificate admitted", err)
	}
}
