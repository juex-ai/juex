//go:build postgres

package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

type memoryFixture struct {
	pool      *pgxpool.Pool
	directory *managementpg.Directory
	store     *memorypg.Store
	service   *memory.Service
	scope     application.Scope
	human     application.Access
	thread    string
}

func managedMemory(t *testing.T) memoryFixture {
	t.Helper()
	ctx := context.Background()
	pool, d := managementDatabase(t)
	if err := memorypg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := memorypg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user, err := d.CreateUser(ctx, "memory@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Memory", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Memory Agent"})
	if err != nil {
		t.Fatal(err)
	}
	authority := managed.RuntimeAuthority{Directory: d}
	human := application.Access{ActorID: user.ID, TenantID: tenant.ID, UserID: user.ID}
	access := human
	access.AgentID = agent.ID
	scope, err := authority.AuthorizeApplication(ctx, access, true)
	if err != nil {
		t.Fatal(err)
	}
	store := memorypg.New(pool)
	return memoryFixture{pool: pool, directory: d, store: store, service: &memory.Service{Repository: store, Authority: authority}, scope: scope, human: human, thread: uuid.NewString()}
}

func (f memoryFixture) proposal(key string) mc.Proposal {
	ref := mc.Source{FleetID: f.scope.FleetID, AgentID: f.scope.AgentID, ThreadID: f.thread, GenerationID: "1", From: 1, Through: 1}
	return mc.Proposal{Key: key, Text: "Remember a supported preference", Sources: []mc.Source{ref}, Evidence: []mc.Evidence{{Source: ref, Kind: "user", Text: "I prefer concise replies.", RecordedAt: time.Now().UTC()}}}
}

func (f memoryFixture) propose(t *testing.T, key string) (memory.Binding, mc.Proposal) {
	t.Helper()
	proposal := f.proposal(key)
	receipt, err := f.service.Propose(context.Background(), f.scope, f.thread, proposal, false)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Status(context.Background(), f.human)
	if err != nil {
		t.Fatal(err)
	}
	return memory.Binding{ReviewID: receipt.ID, Epoch: status.Epoch, Fence: status.Fence}, proposal
}

func memoryDecision(id string, p mc.Proposal) mc.Decision {
	return mc.Decision{Outcome: "applied", Reason: "explicit original user evidence", Changes: []mc.Change{{Entry: mc.Entry{ID: id, Name: "Response preference", Summary: "Prefer concise replies", Type: "user", Body: "The user prefers concise replies.", Sources: p.Sources}}}}
}

func TestManagedMemorySharedKnowledgeAndAuthority(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, proposal := f.propose(t, "remember")
	if duplicate, err := f.service.Propose(ctx, f.scope, f.thread, proposal, false); err != nil || duplicate.ID != binding.ReviewID {
		t.Fatal("proposal replay", duplicate, err)
	}
	changed := proposal
	changed.Text = "Different"
	if _, err := f.service.Propose(ctx, f.scope, f.thread, changed, false); !errors.Is(err, application.ErrConflict) {
		t.Fatal("proposal key reused", err)
	}
	decision := memoryDecision("preference", proposal)
	receipt, err := f.service.Decide(ctx, f.scope, binding, decision)
	if err != nil || !receipt.Committed {
		t.Fatal(receipt, err)
	}
	f.service.Repository = memorypg.New(f.pool)
	if repeated, err := f.service.Decide(ctx, f.scope, binding, decision); err != nil || repeated.ID != receipt.ID {
		t.Fatal("lost response receipt", repeated, err)
	}
	peer, err := f.directory.CreateAgent(ctx, f.human.ActorID, f.human.TenantID, f.human.UserID, management.AgentConfig{Name: "Peer"})
	if err != nil {
		t.Fatal(err)
	}
	peerAccess := f.human
	peerAccess.AgentID = peer.ID
	page, err := f.service.Search(ctx, peerAccess, mc.Query{Text: "concise"})
	if err != nil || len(page.Entries) != 1 || len(page.Entries[0].Sources) != 0 {
		t.Fatal("shared preview", page, err)
	}
	entry, err := f.service.Read(ctx, peerAccess, mc.ReadRequest{ID: "preference"})
	if err != nil || len(entry.Sources) != 1 || entry.Revision != 1 {
		t.Fatal("shared source metadata", entry, err)
	}
	if _, err := f.service.Result(ctx, peerAccess, f.thread, binding.ReviewID); !errors.Is(err, application.ErrDenied) {
		t.Fatal("peer accessed raw proposal receipt", err)
	}
	if _, err := f.service.Administer(ctx, peerAccess, mc.AdminRequest{Key: "delete", Action: "delete", EntryIDs: []string{"preference"}}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Agent invoked human control", err)
	}
	other, err := f.directory.CreateUser(ctx, "other-memory@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.human.ActorID, f.human.TenantID, other.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.directory.AcceptInvitation(ctx, other.ID, token); err != nil {
		t.Fatal(err)
	}
	foreign := application.Access{ActorID: other.ID, TenantID: f.human.TenantID, UserID: f.human.UserID}
	if _, err := f.service.Search(ctx, foreign, mc.Query{}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("cross-user knowledge", err)
	}
	foreign.TenantID = uuid.NewString()
	foreign.ActorID = f.human.ActorID
	if _, err := f.service.Search(ctx, foreign, mc.Query{}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("cross-tenant knowledge", err)
	}
	if _, err := f.directory.SetAgentArchived(ctx, f.human.ActorID, f.human.TenantID, f.scope.AgentID, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Search(ctx, f.scope.Access, mc.Query{}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("archived Agent read", err)
	}
	if page, err := f.service.Search(ctx, peerAccess, mc.Query{}); err != nil || len(page.Entries) != 1 {
		t.Fatal("archive erased shared knowledge", page, err)
	}
}

func TestManagedMemoryDisableAndAdministrativeFences(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, proposal := f.propose(t, "first")
	decision := memoryDecision("preference", proposal)
	if _, err := f.service.Decide(ctx, f.scope, binding, decision); err != nil {
		t.Fatal(err)
	}
	stale, _ := f.propose(t, "pending")
	status, err := f.service.Status(ctx, f.human)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := f.service.Configure(ctx, f.human, status.Version, false, mc.Basic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Search(ctx, f.scope.Access, mc.Query{}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent read", err)
	}
	if page, err := f.service.Search(ctx, f.human, mc.Query{}); err != nil || len(page.Entries) != 1 {
		t.Fatal("disabled read-only UI", page, err)
	}
	if _, err := f.service.Administer(ctx, f.human, mc.AdminRequest{Key: "disabled", Action: "delete", EntryIDs: []string{"preference"}}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled write", err)
	}
	if _, err := f.service.Decide(ctx, f.scope, binding, decision); err != nil {
		t.Fatal("receipt retry changed into mutation", err)
	}
	if _, err := f.service.Configure(ctx, f.human, disabled.Version, true, mc.Basic); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Decide(ctx, f.scope, stale, decision); !errors.Is(err, application.ErrConflict) {
		t.Fatal("old assignment revived", err)
	}
	old, newProposal := f.propose(t, "before-delete")
	request := mc.AdminRequest{Key: "forget", Action: "delete", EntryIDs: []string{"preference"}}
	forgot, err := f.service.Administer(ctx, f.human, request)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.service.Administer(ctx, f.human, request); err != nil || again.ID != forgot.ID {
		t.Fatal("admin receipt replay", again, err)
	}
	if _, err := f.service.Decide(ctx, f.scope, old, memoryDecision("different-id", newProposal)); err == nil {
		t.Fatal("old assignment restored forgotten source under another ID")
	}
	proposal.Key = "after-delete"
	if _, err := f.service.Propose(ctx, f.scope, f.thread, proposal, false); !errors.Is(err, application.ErrDenied) {
		t.Fatal("forgotten source re-extracted", err)
	}
	if _, err := f.service.Decide(ctx, f.scope, binding, decision); err != nil {
		t.Fatal("original commit receipt unavailable", err)
	}
	page, err := f.service.Search(ctx, f.human, mc.Query{})
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("receipt retry restored knowledge", page, err)
	}
	if err := f.store.View(ctx, f.scope, func(state *memory.State) error {
		for _, review := range state.Reviews {
			if len(review.Proposal.Evidence) != 0 || review.Proposal.Text != "" {
				t.Error("forgotten evidence retained")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMemoryFleetCommitSerialization(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	bindings := make([]memory.Binding, 2)
	decisions := make([]mc.Decision, 2)
	for i, id := range []string{"residence-one", "residence-two"} {
		binding, p := f.propose(t, id)
		bindings[i] = binding
		decision := memoryDecision(id, p)
		entry := &decision.Changes[0].Entry
		entry.Entities = []mc.Entity{{ID: "person", Name: "User", Kind: "person"}, {ID: id, Name: id, Kind: "place"}}
		entry.Facts = []mc.Fact{{ID: id, Domain: "identity", Subject: "person", Predicate: "resides_in", Object: id, Status: "valid", SourceType: "user_statement", Sources: p.Sources, RecordedAt: p.Evidence[0].RecordedAt, Reason: "explicit current residence"}}
		decisions[i] = decision
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() { _, err := f.service.Decide(ctx, f.scope, bindings[i], decisions[i]); results <- err })
	}
	wg.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, application.ErrInvalid) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatal("different-entry write skew", succeeded, rejected)
	}
	page, err := f.service.Facts(ctx, f.human, mc.Query{Domain: "identity"})
	if err != nil || len(page.Facts) != 1 {
		t.Fatal(page, err)
	}
}

func TestManagedMemoryDisableOrdersWithReviewCommit(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, p := f.propose(t, "blocked-commit")
	locked, release := make(chan struct{}), make(chan struct{})
	disabled := make(chan error, 1)
	go func() {
		disabled <- f.store.Update(ctx, f.scope, func(state *memory.State) error {
			close(locked)
			<-release
			return state.Configure(state.Control.Version, false, mc.Basic, time.Now())
		})
	}()
	<-locked
	result := make(chan error, 1)
	go func() { _, err := f.service.Decide(ctx, f.scope, binding, memoryDecision("entry", p)); result <- err }()
	select {
	case err := <-result:
		t.Fatal("review bypassed Fleet lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, application.ErrDisabled) {
		t.Fatal("commit survived completed disable", err)
	}
	page, err := f.service.Search(ctx, f.human, mc.Query{})
	if err != nil || len(page.Entries) != 0 {
		t.Fatal(page, err)
	}
}

func TestManagedMemoryNoStoreRelearningAndInvalidDecision(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, proposal := f.propose(t, "original")
	decision := memoryDecision("entry", proposal)
	decision.Changes[0].ExpectedRevision = 7
	if _, err := f.service.Decide(ctx, f.scope, binding, decision); !errors.Is(err, application.ErrConflict) {
		t.Fatal("wrong revision accepted", err)
	}
	if work, err := f.service.Review(ctx, f.scope, binding); err != nil || work.Receipt.Committed {
		t.Fatal("invalid decision settled assignment", work, err)
	}
	decision.Changes[0].ExpectedRevision = 0
	if _, err := f.service.Decide(ctx, f.scope, binding, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Administer(ctx, f.human, mc.AdminRequest{Key: "no-store", Action: "no_store", Sources: proposal.Sources}); err != nil {
		t.Fatal(err)
	}
	proposal.Key = "blocked-replay"
	if _, err := f.service.Propose(ctx, f.scope, f.thread, proposal, false); !errors.Is(err, application.ErrDenied) {
		t.Fatal("no-store source admitted", err)
	}
	if _, err := f.service.Administer(ctx, f.human, mc.AdminRequest{Key: "relearn", Action: "allow_store", EntryIDs: []string{"entry"}, Sources: proposal.Sources}); err != nil {
		t.Fatal(err)
	}
	proposal.Key = "explicitly-relearned"
	receipt, err := f.service.Propose(ctx, f.scope, f.thread, proposal, false)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Status(ctx, f.human)
	if err != nil {
		t.Fatal(err)
	}
	newBinding := memory.Binding{ReviewID: receipt.ID, Epoch: status.Epoch, Fence: status.Fence}
	if _, err := f.service.Decide(ctx, f.scope, newBinding, decision); err != nil {
		t.Fatal(err)
	}
	if entry, err := f.service.Read(ctx, f.human, mc.ReadRequest{ID: "entry"}); err != nil || entry.Revision != 1 {
		t.Fatal(entry, err)
	}
	spoofed := proposal
	spoofed.Key = "fake-evidence"
	spoofed.Evidence = append([]mc.Evidence(nil), proposal.Evidence...)
	spoofed.Evidence[0].Kind = "tool"
	if _, err := f.service.Propose(ctx, f.scope, f.thread, spoofed, false); !errors.Is(err, application.ErrInvalid) {
		t.Fatal("tool output became original evidence", err)
	}
	spoofed = proposal
	spoofed.Key = "another-thread"
	spoofed.Sources = append([]mc.Source(nil), proposal.Sources...)
	spoofed.Sources[0].ThreadID = uuid.NewString()
	if _, err := f.service.Propose(ctx, f.scope, f.thread, spoofed, false); !errors.Is(err, application.ErrDenied) {
		t.Fatal("another Thread evidence admitted", err)
	}
}

func TestManagedMemoryColdKnowledgeRemainsSearchable(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	for offset := 0; offset < 205; offset += 20 {
		request := mc.AdminRequest{Key: fmt.Sprint("batch-", offset), Action: "correct"}
		for i := offset; i < min(offset+20, 205); i++ {
			request.Changes = append(request.Changes, mc.Change{Entry: mc.Entry{ID: fmt.Sprintf("entry-%03d", i), Name: "Cold knowledge", Summary: "Retained reference", Type: "reference", Body: fmt.Sprintf("Original retained fact %03d", i)}})
		}
		if _, err := f.service.Administer(ctx, f.human, request); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.service.Search(ctx, f.scope.Access, mc.Query{Offset: 200, Limit: 20})
	if err != nil || len(page.Entries) != 5 || page.Next != -1 {
		t.Fatal("hot index size became a storage limit", page, err)
	}
	if entry, err := f.service.Read(ctx, f.scope.Access, mc.ReadRequest{ID: "entry-000"}); err != nil || entry.Body != "Original retained fact 000" {
		t.Fatal("cold knowledge unavailable", entry, err)
	}
}
