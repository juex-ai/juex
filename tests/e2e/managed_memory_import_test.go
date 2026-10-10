//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

func TestManagedMemoryReviewKeyMigrationPreservesNamespaces(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	if _, err := f.service.Configure(ctx, f.human, 1, true, mc.Advanced); err != nil {
		t.Fatal(err)
	}
	proposal := f.proposal("shared-key")
	automatic, err := f.service.Propose(ctx, f.scope, f.thread, proposal, true, "automatic-before-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	oldKey := f.scope.AgentID + "/" + f.thread + "/" + proposal.Key
	// Schema 3 used the same key shape for automatic and explicit proposals.
	if _, err := f.pool.Exec(ctx, `UPDATE memory.fleets SET state=jsonb_set(state,'{keys}',jsonb_build_object($1::text,$2::text))`, oldKey, automatic.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM memory.schema_versions WHERE version=4`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := memorypg.Migrate(ctx, f.pool); err != nil {
			t.Fatal(err)
		}
	}
	f.service.Repository = memorypg.New(f.pool)
	if receipt, err := f.service.Propose(ctx, f.scope, f.thread, proposal, true, "automatic-after-upgrade"); err != nil || receipt.ID != automatic.ID {
		t.Fatal("upgrade lost existing automatic identity", receipt, err)
	}
	explicit, err := f.service.Propose(ctx, f.scope, f.thread, proposal, false, "explicit-after-upgrade")
	if err != nil || explicit.ID == automatic.ID {
		t.Fatal("automatic and explicit proposal keys collided", explicit, err)
	}
	if err := f.store.View(ctx, f.scope, func(s *memory.State) error {
		if len(s.Keys) != 2 || s.Keys[oldKey] != explicit.ID || s.Keys["automatic/"+oldKey] != automatic.ID {
			t.Fatal("migration left an alias or rewrote receipts", s.Keys)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMemoryImportRejectsExpandedStateAtomically(t *testing.T) {
	f := managedMemory(t)
	value := memoryImportFixture(f)
	review := value.Reviews[0]
	review.Proposal.Text = strings.Repeat("x", 31<<10)
	review.Proposal.Key, review.Receipt.ID = "000000000000", "000000000000"
	value.Reviews = nil
	base, _ := json.Marshal(value)
	item, _ := json.Marshal(review)
	const capacity = 64 << 20
	count := (capacity - 8192 - len(base)) / (len(item) + 1)
	for i := range count {
		copyReview := review
		copyReview.Proposal.Key, copyReview.Receipt.ID = fmt.Sprintf("%012d", i), fmt.Sprintf("%012d", i)
		value.Reviews = append(value.Reviews, copyReview)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) >= capacity {
		t.Fatal("fixture does not fit input capacity", len(encoded), err)
	}
	state, err := value.BuildState(fleetOwner(f))
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := json.Marshal(state)
	if err != nil || len(expanded) <= capacity {
		t.Fatal("fixture does not cross persisted capacity", len(expanded), err)
	}
	if err := f.store.ImportFleet(context.Background(), fleetOwner(f), value); !errors.Is(err, application.ErrConflict) {
		t.Fatal("oversized state imported", err)
	}
	var fleets, imports int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM memory.fleets),(SELECT count(*) FROM memory.imports)`).Scan(&fleets, &imports); err != nil || fleets != 0 || imports != 0 {
		t.Fatal("oversized import left partial state", fleets, imports, err)
	}
}

func memoryImportFixture(f memoryFixture) memory.FleetImport {
	now := time.Now().UTC().Add(-30 * 24 * time.Hour)
	proposal := f.proposal("source-request")
	proposal.Evidence[0].RecordedAt = now
	entry := memoryDecision("imported-preference", proposal).Changes[0].Entry
	entry.Revision, entry.CreatedAt, entry.UpdatedAt = 7, now, now
	receipt := mc.Receipt{ID: "old-review", State: "applied", Committed: true, IndexReady: true, EntryIDs: []string{entry.ID}, Attempts: 3, UpdatedAt: now}
	source := memory.HistoricalSource{Scope: f.scope, ThreadID: f.thread, SourceSHA256: strings.Repeat("a", 64), Epoch: "source-epoch", AcceptedThrough: 3, ProcessedThrough: 1, Enabled: false}
	evidence := proposal.Evidence[0]
	evidence.Kind, evidence.Text, evidence.Source.From, evidence.Source.Through = "assistant", "Old assistant evidence", 2, 3
	source.Evidence = []mc.Evidence{evidence}
	return memory.FleetImport{Source: "fixture/fleet", SourceSHA256: strings.Repeat("b", 64), Control: application.Control{Enabled: true, Epoch: 1, Version: 1}, Fence: 1, Strategy: mc.Basic, AdvancedSince: time.Now().UTC(), Entries: []mc.Entry{entry}, Reviews: []memory.ImportedReview{{Scope: f.scope, ThreadID: f.thread, Proposal: proposal, Receipt: receipt, RetainKey: true, History: memory.ReviewHistory{SourceSHA256: strings.Repeat("c", 64), Fingerprint: "old-fingerprint", DecisionHash: "old-decision"}}}, Sources: []memory.HistoricalSource{source}}
}
func fleetOwner(f memoryFixture) application.Scope {
	s := f.scope
	s.AgentID = ""
	s.AgentEpoch = 0
	return s
}

type importedMemoryEffects struct{ calls atomic.Int32 }

func (g *importedMemoryEffects) Admit(context.Context, memory.Review) (memory.WorkerState, error) {
	g.calls.Add(1)
	return memory.WorkerState{}, nil
}
func (g *importedMemoryEffects) State(context.Context, memory.Review) (memory.WorkerState, error) {
	g.calls.Add(1)
	return memory.WorkerState{}, nil
}
func (g *importedMemoryEffects) Cancel(context.Context, memory.Review) error {
	g.calls.Add(1)
	return nil
}
func (g *importedMemoryEffects) Main(context.Context, application.Event) error {
	g.calls.Add(1)
	return nil
}
func (g *importedMemoryEffects) Inbox(context.Context, application.Event) error {
	g.calls.Add(1)
	return nil
}

func TestManagedMemoryImportRetainsBusinessHistoryWithoutReplay(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	owner := fleetOwner(f)
	value := memoryImportFixture(f)
	// A harmless earlier status initializes the same empty state accepted by import.
	if _, err := f.service.Status(ctx, f.human); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ImportFleet(ctx, owner, value); err != nil {
		t.Fatal(err)
	}
	f.store = memorypg.New(f.pool)
	f.service.Repository = f.store
	effects := &importedMemoryEffects{}
	f.service.Workers, f.service.Notifier = effects, effects
	for range 3 {
		if err := f.service.Step(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.store.Update(ctx, owner, func(*memory.State) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if effects.calls.Load() != 0 {
		t.Fatal("import replayed work or notification", effects.calls.Load())
	}
	entry, err := f.service.Read(ctx, f.human, mc.ReadRequest{ID: value.Entries[0].ID, View: "stored"})
	if err != nil || entry.Revision != 7 || !entry.UpdatedAt.Equal(value.Entries[0].UpdatedAt) {
		t.Fatal("entry rewritten", entry.Revision, err)
	}
	reviews, err := f.service.Reviews(ctx, f.human, 0, 50)
	if err != nil || len(reviews.Reviews) != 1 || reviews.Reviews[0].State != "applied" || reviews.Reviews[0].Attempts != 3 || reviews.Reviews[0].WorkerID != "" {
		t.Fatal("historical outcome or Worker invented", reviews, err)
	}
	binding := memory.Binding{ReviewID: "old-review", Epoch: 1, Fence: 1}
	if _, err := f.service.Review(ctx, f.scope, binding); !errors.Is(err, application.ErrDenied) {
		t.Fatal("historical binding authorized", err)
	}
	if _, err := f.service.Decide(ctx, f.scope, binding, mc.Decision{}, "old-decision"); !errors.Is(err, application.ErrDenied) {
		t.Fatal("historical decision replayed", err)
	}
	if receipt, err := f.service.Propose(ctx, f.scope, f.thread, value.Reviews[0].Proposal, false, "repeat-old-request"); err != nil || receipt.ID != "old-review" {
		t.Fatal("proposal key created another job", receipt, err)
	}
	if _, err := f.service.Configure(ctx, f.human, 1, true, mc.Advanced); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Maintain(ctx, f.scope, f.thread, "requested maintenance", "blocked-maintain"); !errors.Is(err, application.ErrDenied) {
		t.Fatal("disabled source accepted maintenance", err)
	}
	e := value.Reviews[0].Proposal.Evidence[0]
	e.Source.From, e.Source.Through = 4, 4
	e.RecordedAt = time.Now().UTC()
	if err := f.service.Contribute(ctx, f.scope, memory.Contribution{Epoch: 2, Evidence: e}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.View(ctx, owner, func(s *memory.State) error {
		if len(s.Participation) != 0 || len(s.ImportedSources[f.scope.AgentID+"/"+f.thread].Evidence) != 1 || s.Reviews["old-review"].Proposal.Text == "" {
			t.Fatal("configuration or retention changed imported evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ImportFleet(ctx, owner, value); err != nil {
		t.Fatal("exact retry rejected after new config", err)
	}
	status, err := f.service.Status(ctx, f.human)
	if err != nil || status.Strategy != mc.Advanced || status.Version != 2 {
		t.Fatal("retry overwrote new state", status, err)
	}
	for range 2 {
		if err := f.service.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if effects.calls.Load() != 0 {
		t.Fatal("later configuration revived imported work")
	}
	source := value.Sources[0].Evidence[0].Source
	if _, err := f.service.Administer(ctx, f.human, mc.AdminRequest{Key: "forget", Action: "no_store", Sources: []mc.Source{source}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.View(ctx, owner, func(s *memory.State) error {
		if len(s.ImportedSources[f.scope.AgentID+"/"+f.thread].Evidence) != 0 {
			t.Fatal("forget retained imported evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMemoryImportAtomicityOwnershipAndPurge(t *testing.T) {
	for _, scenario := range []string{"concurrent", "nonempty", "foreign", "invalid", "agent-purge", "fleet-purge"} {
		t.Run(scenario, func(t *testing.T) {
			f := managedMemory(t)
			ctx := context.Background()
			owner := fleetOwner(f)
			value := memoryImportFixture(f)
			switch scenario {
			case "nonempty":
				if _, err := f.service.Configure(ctx, f.human, 1, false, mc.Basic); err != nil {
					t.Fatal(err)
				}
				if err := f.store.ImportFleet(ctx, owner, value); !errors.Is(err, application.ErrConflict) {
					t.Fatal("nonempty control overwritten", err)
				}
				return
			case "foreign":
				foreign := owner
				foreign.UserID = uuid.NewString()
				value.Reviews[0].Scope.UserID = foreign.UserID
				value.Sources[0].Scope.UserID = foreign.UserID
				if _, err := f.service.Status(ctx, f.human); err != nil {
					t.Fatal(err)
				}
				if err := f.store.ImportFleet(ctx, foreign, value); !errors.Is(err, application.ErrDenied) {
					t.Fatal("foreign import took ownership", err)
				}
				return
			case "invalid":
				value.Reviews[0].Receipt.State = "pending"
				if err := f.store.ImportFleet(ctx, owner, value); !errors.Is(err, application.ErrInvalid) {
					t.Fatal("live work imported", err)
				}
				var count int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM memory.fleets`).Scan(&count); err != nil || count != 0 {
					t.Fatal("invalid import left partial state", count, err)
				}
				return
			}
			var wg sync.WaitGroup
			results := make(chan error, 4)
			for range 4 {
				wg.Go(func() { results <- f.store.ImportFleet(ctx, owner, value) })
			}
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatal(err)
				}
			}
			changed := value
			changed.SourceSHA256 = strings.Repeat("d", 64)
			if err := f.store.ImportFleet(ctx, owner, changed); !errors.Is(err, application.ErrConflict) {
				t.Fatal("different snapshot accepted", err)
			}
			if scenario == "concurrent" {
				return
			}
			target := lifecycle.Target{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, FleetID: owner.FleetID, AgentIDs: []string{f.scope.AgentID}, WholeFleet: scenario == "fleet-purge"}
			if _, err := f.store.Purge(ctx, lifecycle.Request{Target: target, Phase: lifecycle.Erase}); err != nil {
				t.Fatal(err)
			}
			if err := f.store.ImportFleet(ctx, owner, value); !errors.Is(err, application.ErrDenied) {
				t.Fatal("purge followed by successful retry", err)
			}
			var data []byte
			if target.WholeFleet {
				var count int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM memory.imports`).Scan(&count); err != nil || count != 0 {
					t.Fatal("Fleet provenance survived purge", count, err)
				}
			} else {
				if err := f.pool.QueryRow(ctx, `SELECT state::text FROM memory.fleets WHERE id=$1`, owner.FleetID).Scan(&data); err != nil || strings.Contains(string(data), "Old assistant evidence") || strings.Contains(string(data), "old-review") {
					t.Fatal("Agent purge retained private import material", err)
				}
			}
		})
	}
}
