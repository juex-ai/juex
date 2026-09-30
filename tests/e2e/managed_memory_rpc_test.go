//go:build postgres

package e2e

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/application"
	applicationrpc "github.com/juex-ai/juex/internal/foundation/application/rpc"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/memory"
	memoryrpc "github.com/juex-ai/juex/internal/memory/rpc"
)

func TestManagedMemoryIndependentKitexServices(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	managementListener := platformListener(t)
	managementServer, err := serverrpc.NewManagement(managementListener, platformrpc.CredentialsAt(pki, "management"), managed.RuntimeAuthority{Directory: f.directory})
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, managementServer)
	authority, err := applicationrpc.NewAuthority(managementListener.Addr().String(), platformrpc.CredentialsAt(pki, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	f.service.Authority = authority
	listener := platformListener(t)
	server, err := serverrpc.NewMemory(listener, platformrpc.CredentialsAt(pki, "memory"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := memoryrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := memoryrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	status, err := client.Status(ctx, f.human)
	if err != nil || !status.Enabled {
		t.Fatal(status, err)
	}
	proposal := f.proposal("rpc")
	if _, err := client.Propose(ctx, f.scope, f.thread, proposal, false, proposal.Key); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management invented original Runtime evidence", err)
	}
	if err := client.CancelCommand(ctx, f.scope, "cancel-rpc"); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management used prepared tool cancellation", err)
	}
	if err := runtime.CancelCommand(ctx, f.scope, "cancel-rpc"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Propose(ctx, f.scope, f.thread, proposal, false, "cancel-rpc"); !errors.Is(err, application.ErrDenied) {
		t.Fatal("RPC cancellation lost", err)
	}
	if _, err := runtime.Administer(ctx, f.human, mc.AdminRequest{Key: "fake-user", Action: "delete", EntryIDs: []string{"entry"}}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Runtime forged human administration", err)
	}
	if _, err := client.Search(ctx, f.scope.Access, mc.Query{}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management forged Agent tool call", err)
	}
	receipt, err := runtime.Propose(ctx, f.scope, f.thread, proposal, false, proposal.Key)
	if err != nil {
		t.Fatal(err)
	}
	binding := memory.Binding{ReviewID: receipt.ID, Epoch: status.Epoch, Fence: status.Fence}
	work, err := runtime.Review(ctx, f.scope, binding)
	if err != nil || len(work.Proposal.Evidence) != 1 {
		t.Fatal(work, err)
	}
	if _, err := client.Review(ctx, f.scope, binding); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management read raw assignment capability", err)
	}
	if domains, err := runtime.Domains(ctx, f.scope.Access, mc.DomainRequest{}); err != nil || len(domains) == 0 {
		t.Fatal(domains, err)
	}
	decision := memoryDecision("rpc-entry", proposal)
	if result, err := runtime.Decide(ctx, f.scope, binding, decision, "decide-"+binding.ReviewID); err != nil || !result.Committed {
		t.Fatal(result, err)
	}
	if result, err := client.Result(ctx, f.human, "", receipt.ID); err != nil || !result.Committed {
		t.Fatal(result, err)
	}
	if page, err := client.Search(ctx, f.human, mc.Query{}); err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	if entry, err := client.Read(ctx, f.human, mc.ReadRequest{ID: "rpc-entry"}); err != nil || entry.Revision != 1 {
		t.Fatal(entry, err)
	}
	if facts, err := client.Facts(ctx, f.human, mc.Query{}); err != nil || len(facts.Facts) != 0 {
		t.Fatal(facts, err)
	}
	if _, err := client.Configure(ctx, f.human, status.Version, false, mc.Basic); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Search(ctx, f.scope.Access, mc.Query{}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled RPC tools", err)
	}
	if page, err := client.Search(ctx, f.human, mc.Query{}); err != nil || len(page.Entries) != 1 {
		t.Fatal("disabled RPC UI read", page, err)
	}
	execution, err := memoryrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Health(ctx); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal("execution endpoint reached Memory service", err)
	}
}
