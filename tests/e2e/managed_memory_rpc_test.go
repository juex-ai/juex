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
	if _, err := runtime.Reviews(ctx, f.scope.Access, 0, 20); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Runtime read human review overview", err)
	}
	if _, err := runtime.StorageRules(ctx, f.scope.Access, 0, 20); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Runtime read human suppression rules", err)
	}
	if rules, err := client.StorageRules(ctx, f.human, 0, 20); err != nil || len(rules.Entries)+len(rules.Sources) != 0 {
		t.Fatal(rules, err)
	}
	proposal := f.proposal("rpc")
	if err := client.Contribute(ctx, f.scope, memory.Contribution{Epoch: status.Epoch, Evidence: proposal.Evidence[0]}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management invented automatic evidence", err)
	}
	if _, err := client.Maintain(ctx, f.scope, f.thread, "user requested", "maintain"); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management forged maintenance tool", err)
	}
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
	invalid := memoryDecision("bad entry ID", proposal)
	if _, err := runtime.Decide(ctx, f.scope, binding, invalid, "bad-then-correct"); !errors.Is(err, application.ErrInvalid) || err.Error() == application.ErrInvalid.Error() {
		t.Fatal("business validation detail lost over RPC", err)
	}
	if result, err := runtime.Decide(ctx, f.scope, binding, decision, "decide-"+binding.ReviewID); err != nil || !result.Committed {
		t.Fatal(result, err)
	}
	if result, err := client.Result(ctx, f.human, "", receipt.ID); err != nil || !result.Committed {
		t.Fatal(result, err)
	}
	if reviews, err := client.Reviews(ctx, f.human, 0, 20); err != nil || len(reviews.Reviews) != 1 || !reviews.Reviews[0].Committed {
		t.Fatal(reviews, err)
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
	status, err = client.Configure(ctx, f.human, status.Version+1, true, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	proposal = f.proposal("advanced-rpc")
	if err := runtime.Contribute(ctx, f.scope, memory.Contribution{Epoch: status.Epoch, Evidence: proposal.Evidence[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Maintain(ctx, f.scope, f.thread, "user requested", "maintain"); err != nil {
		t.Fatal(err)
	}
	execution, err := memoryrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Health(ctx); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal("execution endpoint reached Memory service", err)
	}
}
