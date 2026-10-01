//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

func TestManagedRuntimeAutomaticReviewIdleAdmission(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	job := applicationJob()
	job.IdleSourceThread = main.ID
	if _, err := store.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrSourceBusy) {
		t.Fatal("fresh Thread admitted", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.application_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("busy admission persisted a job", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.threads SET updated_at=clock_timestamp()-interval '61 seconds' WHERE id=$1`, main.ID); err != nil {
		t.Fatal(err)
	}
	r, err := store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: uuid.NewString(), ThreadID: main.ID, Text: "New user work"}); err != nil {
		t.Fatal(err)
	}
	if retry, err := store.AdmitApplication(ctx, scope, job); err != nil || retry.InputID != r.InputID {
		t.Fatal("accepted job lost after new source activity", retry, err)
	}
	next := applicationJob()
	next.IdleSourceThread = main.ID
	if _, err := store.AdmitApplication(ctx, scope, next); !errors.Is(err, managedruntime.ErrSourceBusy) {
		t.Fatal("second Fleet review admitted", err)
	}
	if err := store.CancelApplication(ctx, scope, "memory", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.inputs SET state='held' WHERE thread_id=$1`, main.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.threads SET state='idle',updated_at=clock_timestamp()-interval '61 seconds' WHERE id=$1`, main.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitApplication(ctx, scope, next); !errors.Is(err, managedruntime.ErrSourceBusy) {
		t.Fatal("held input ignored", err)
	}
}

func TestManagedRuntimeMemoryOutboxNeverSkipsThreadHead(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	first, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: uuid.NewString(), ThreadID: main.ID, Text: "first original input"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: uuid.NewString(), ThreadID: main.ID, Text: "second original input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.memory_evidence(input_id) VALUES($1),($2)`, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	// Cursor order, not wall clock or UUID sorting, is the durable source order.
	if _, err := pool.Exec(ctx, `UPDATE runtime.inputs SET accepted_at=clock_timestamp()+interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT input_id FROM runtime.memory_evidence WHERE input_id=$1 FOR UPDATE`, first.ID); err != nil {
		t.Fatal(err)
	}
	if values, err := store.PendingEvidence(ctx, 100); err != nil || len(values) != 0 {
		t.Fatal("locked head was skipped", values, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	values, err := store.PendingEvidence(ctx, 100)
	if err != nil || len(values) != 1 || values[0].ID != first.ID || values[0].Text != "first original input" {
		t.Fatal(values, err)
	}
	if values, err := store.PendingEvidence(ctx, 100); err != nil || len(values) != 0 {
		t.Fatal("in-flight head was skipped", values, err)
	}
	if err := store.FinishEvidence(ctx, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if values, err := store.PendingEvidence(ctx, 100); err != nil || len(values) != 1 || values[0].ID != second.ID {
		t.Fatal("head completion did not release successor", values, err)
	}
}

func TestManagedMemoryManualMaintenanceBeforeEvidenceAndFailureDisposition(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	status, err := f.service.Configure(ctx, f.human, 1, true, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.service.Maintain(ctx, f.scope, f.thread, "User requested maintenance", "manual")
	if err != nil || receipt.State != "pending" {
		t.Fatal(receipt, err)
	}
	if retry, err := f.service.Maintain(ctx, f.scope, f.thread, "User requested maintenance", "manual"); err != nil || retry.ID != receipt.ID {
		t.Fatal("manual request duplicated", retry, err)
	}
	if pending, err := f.store.PendingParticipation(ctx, 100); err != nil || len(pending) != 0 {
		t.Fatal("empty manual request broke background scan", pending, err)
	}
	proposal := f.proposal("manual-evidence")
	if err := f.service.Contribute(ctx, f.scope, memory.Contribution{Epoch: status.Epoch, Evidence: proposal.Evidence[0]}); err != nil {
		t.Fatal(err)
	}
	var review *memory.Review
	if err := f.store.Update(ctx, f.scope, func(state *memory.State) error {
		r, err := state.Advance(f.scope.AgentID+"/"+f.thread, time.Now())
		review = state.Reviews[r.ID]
		return err
	}); err != nil || review == nil {
		t.Fatal(review, err)
	}
	binding := memory.Binding{ReviewID: review.ID, Epoch: review.Epoch, Fence: review.Fence}
	if _, err := f.service.Decide(ctx, f.scope, binding, mc.Decision{Outcome: "rejected", Reason: "No durable fact"}, "reject"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Update(ctx, f.scope, func(state *memory.State) error { state.Reviews[review.ID].WorkerFinished = true; return nil }); err != nil {
		t.Fatal(err)
	}
	next := proposal.Evidence[0]
	next.Source.From, next.Source.Through = 2, 2
	next.Text = "A genuinely new preference"
	if err := f.service.Contribute(ctx, f.scope, memory.Contribution{Epoch: status.Epoch, Evidence: next}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Update(ctx, f.scope, func(state *memory.State) error {
		p := state.Participation[f.scope.AgentID+"/"+f.thread]
		if p.ProcessedThrough != 0 || len(p.Evidence) != 1 || p.Evidence[0].Source.From != 2 {
			t.Fatal("failed range reintroduced", p)
		}
		r, err := state.Advance(f.scope.AgentID+"/"+f.thread, time.Now().Add(25*time.Hour))
		if err == nil && len(state.Reviews[r.ID].Proposal.Evidence) != 1 {
			t.Fatal("failed evidence repackaged")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMemoryAdvancedOriginalEvidenceOutbox(t *testing.T) {
	ctx := context.Background()
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) { streamManagedReply(w, "Conversation completed") })
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	repo := memorypg.New(f.pool)
	svc := &memory.Service{Repository: repo, Authority: f.authority}
	human := application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor}
	if _, err := svc.Configure(ctx, human, 1, true, mc.Advanced); err != nil {
		t.Fatal(err)
	}
	gateway := managed.RuntimeApplications{Memory: svc, Evidence: f.store}
	f.service.Applications = gateway
	svc.Workers = managed.MemoryWorkers{Runtime: f.service}
	stop := runApplicationFixture(t, f, gateway)
	for i := 0; i < 5; i++ {
		f.submit(t, uuid.NewString(), "", "I prefer concise replies.")
	}
	for accepted := 1; accepted <= 5; accepted++ {
		runtimeEventually(t, func() bool {
			items, err := repo.PendingParticipation(ctx, 100)
			return err == nil && len(items) == 1 && len(items[0].Evidence) >= accepted
		})
	}
	stop()
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.PendingReviews(ctx, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	job := jobs[0]
	if !job.Automatic || job.WorkerID != "" || len(job.Proposal.Evidence) != 5 {
		t.Fatal("automatic review bypassed idle wait", job)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.threads SET updated_at=clock_timestamp()-interval '61 seconds' WHERE id=$1`, f.main.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err = repo.PendingReviews(ctx, 100)
	if err != nil || len(jobs) != 1 || jobs[0].WorkerID == "" {
		t.Fatal("idle automatic review not admitted", jobs, err)
	}
	for _, e := range job.Proposal.Evidence {
		if e.Kind != "user" || e.Text != "I prefer concise replies." {
			t.Fatal("injected evidence", e)
		}
	}
	// A failed batch remains a terminal receipt; future input does not retry it.
	if err := f.service.CancelApplication(ctx, toRuntimeScope(job.Scope), "memory", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if items, err := repo.PendingParticipation(ctx, 100); err != nil || len(items) != 0 {
		t.Fatal("failed range still automatically eligible", items, err)
	}
	if result, err := svc.Result(ctx, job.Scope.Access, job.ThreadID, job.ID); err != nil || result.State != "failed" {
		t.Fatal(result, err)
	}
}

func toRuntimeScope(s application.Scope) managedruntime.Scope {
	return managedruntime.Scope{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, FleetID: s.FleetID, AgentID: s.AgentID, ActorAuthorizationEpoch: s.ActorEpoch, MembershipExecutionEpoch: s.MemberEpoch, AgentExecutionEpoch: s.AgentEpoch, MembershipVersion: s.MemberVersion}
}

func TestManagedMemoryAdvancedCapacityDrainAndConfigurationBoundary(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	status, err := f.service.Configure(ctx, f.human, 1, true, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	ref := mc.Source{FleetID: f.scope.FleetID, AgentID: f.scope.AgentID, ThreadID: f.thread, GenerationID: "1", From: 1, Through: 1}
	batch := memory.Contribution{Epoch: status.Epoch, Evidence: mc.Evidence{Source: ref, Kind: "user", Text: strings.Repeat("x", 20<<10), RecordedAt: time.Now()}}
	if err := f.service.Contribute(ctx, f.scope, batch); err != nil {
		t.Fatal(err)
	}
	batch.Evidence.Source.From, batch.Evidence.Source.Through = 2, 2
	if err := f.service.Contribute(ctx, f.scope, batch); !errors.Is(err, application.ErrConflict) {
		t.Fatal("overfull evidence acknowledged", err)
	}
	if err := f.service.Repository.View(ctx, f.scope, func(s *memory.State) error {
		p := s.Participation[f.scope.AgentID+"/"+f.thread]
		if !p.Manual || len(p.Evidence) != 1 {
			t.Fatal("drain flag rolled back", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	status, err = f.service.Configure(ctx, f.human, status.Version, false, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	status, err = f.service.Configure(ctx, f.human, status.Version, true, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	batch.Epoch = status.Epoch
	if err := f.service.Contribute(ctx, f.scope, batch); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Repository.View(ctx, f.scope, func(s *memory.State) error {
		if len(s.Participation) != 0 {
			t.Fatal("old accepted input backfilled after reenable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMemoryTerminalEvidenceRetention(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, proposal := f.propose(t, "retention")
	if _, err := f.service.Decide(ctx, f.scope, binding, memoryDecision("retained-knowledge", proposal), "retained-decision"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Update(ctx, f.scope, func(state *memory.State) error {
		review := state.Reviews[binding.ReviewID]
		review.WorkerFinished = true
		review.Receipt.UpdatedAt = time.Now().Add(-8 * 24 * time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-7 * 24 * time.Hour)
	scopes, err := f.store.ExpiredReviewEvidence(ctx, before, 20)
	if err != nil || len(scopes) != 1 {
		t.Fatal(scopes, err)
	}
	if err := f.store.Update(ctx, scopes[0], func(state *memory.State) error { state.PruneEvidence(before); return nil }); err != nil {
		t.Fatal(err)
	}
	if scopes, err := f.store.ExpiredReviewEvidence(ctx, before, 20); err != nil || len(scopes) != 0 {
		t.Fatal("terminal payload not pruned", scopes, err)
	}
	if receipt, err := f.service.Decide(ctx, f.scope, binding, memoryDecision("retained-knowledge", proposal), "retained-decision"); err != nil || !receipt.Committed {
		t.Fatal("receipt lost after evidence expiry", receipt, err)
	}
	if entry, err := f.service.Read(ctx, f.scope.Access, mc.ReadRequest{ID: "retained-knowledge"}); err != nil || len(entry.Sources) != 1 {
		t.Fatal("knowledge provenance lost", entry, err)
	}
}
