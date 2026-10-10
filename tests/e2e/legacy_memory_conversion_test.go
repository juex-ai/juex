//go:build postgres

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestLegacyMemoryConversionPreservesAPIProvenanceWithoutReplay(t *testing.T) {
	ctx := context.Background()
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("historical Memory invoked provider") })
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Imported Memory source", Configuration: &management.Configuration{Models: f.agent.Configuration.Models}})
	if err != nil {
		t.Fatal(err)
	}
	runtimeScope, err := f.authority.Authorize(ctx, f.actor, f.tenant, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor}, true)
	if err != nil {
		t.Fatal(err)
	}
	source := legacyRuntimeSource("The user prefers concise replies.")
	runtime, err := migration.ConvertRuntime(runtimeScope, source, migration.RuntimeBindings{SourceSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	ref := mc.Source{FleetID: "original-fleet", AgentID: source.Definition.ID, ThreadID: "0", GenerationID: "g000001", From: 2, Through: 2}
	proposal := mc.Proposal{Key: "original-key", Text: "Remember concise replies", Sources: []mc.Source{ref}, Evidence: []mc.Evidence{{Source: ref, Text: "The user prefers concise replies.", Kind: "user", RecordedAt: at}}}
	entry := memoryDecision("imported-preference", proposal).Changes[0].Entry
	entry.Revision, entry.CreatedAt, entry.UpdatedAt = 7, at, at
	caller := legacy.MemoryCaller{FleetID: ref.FleetID, AgentID: ref.AgentID, ThreadID: ref.ThreadID, Profile: "agent"}
	work := &legacy.MemoryWork{Caller: caller, Proposal: proposal, Receipt: mc.Receipt{ID: "original-review", State: "failed", Attempts: 3, UpdatedAt: at}, Fingerprint: "original-fingerprint"}
	work.Automatic, work.Proposal.Key = true, "maintenance/abcdef/0/source-epoch/0/4"
	retry := *work
	retry.Receipt.ID, retry.Receipt.State = "retry-review", "no_change"
	admin := &legacy.MemoryWork{Caller: legacy.MemoryCaller{FleetID: ref.FleetID, AgentID: "user", ThreadID: "0", Profile: "user"}, Proposal: mc.Proposal{Key: "forget"}, Receipt: mc.Receipt{ID: "human-action", State: "applied", Committed: true, EntryIDs: []string{"forgotten"}, UpdatedAt: at}, Fingerprint: "source-admin-fingerprint"}
	state := legacy.MemoryState{Fleet: ref.FleetID, Strategy: mc.Basic, Requests: map[string]*legacy.MemoryWork{work.Receipt.ID: work}, Sources: map[string]*legacy.MemorySource{"abcdef/0": {Caller: caller, Epoch: "source-epoch", Enabled: false, AcceptedThrough: 4, Evidence: proposal.Evidence}}, Deleted: map[string]bool{"forgotten": true}}
	state.Requests[retry.Receipt.ID], state.Requests[admin.Receipt.ID] = &retry, admin
	state.Keys = map[string]string{work.Proposal.Key: retry.Receipt.ID, "admin/forget": admin.Receipt.ID}
	suppressed := ref
	suppressed.GenerationID, suppressed.From, suppressed.Through = "", 3, 4
	state.Suppressed = []mc.Source{suppressed}
	fleet := legacy.Fleet{ID: ref.FleetID, Agents: []legacy.Agent{source}, Memory: &legacy.Memory{State: state, Entries: []mc.Entry{entry}}}
	value, err := migration.ConvertMemory(owner, fleet, migration.MemoryBindings{SourceSHA256: strings.Repeat("b", 64), Control: application.Control{Enabled: true, Epoch: 1, Version: 1}, AdvancedSince: time.Now().UTC(), Agents: map[string]migration.MemoryAgentBinding{source.Definition.ID: {Scope: scope, Runtime: runtime}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	var noticesBefore int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.notification_outbox`).Scan(&noticesBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ImportAgent(ctx, runtimeScope, runtime.Import); err != nil {
		t.Fatal(err)
	}
	if err := memorypg.New(f.pool).ImportFleet(ctx, owner, value); err != nil {
		t.Fatal(err)
	}
	effects := &importedMemoryEffects{}
	svc := &memory.Service{Repository: memorypg.New(f.pool), Authority: f.authority, Workers: effects, Notifier: effects}
	for range 3 {
		if err := svc.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Runtime: f.service, Memory: svc, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	base := origin + "/api/tenants/" + f.tenant + "/users/" + f.actor + "/memory"
	stored := managementCall[mc.Entry](t, f.client, "GET", base+"/entries/"+entry.ID+"?view=stored", origin, nil, 200)
	span := runtime.CommitSpans["0"][2]
	if stored.Revision != 7 || !stored.UpdatedAt.Equal(at) || len(stored.Sources) != 1 || stored.Sources[0].From != uint64(span.First) || stored.Sources[0].Through != uint64(span.Last) {
		t.Fatal("API lost original revision/time or narrowed provenance", stored)
	}
	main := runtime.Identities.Threads["0"]
	timeline := managementCall[managedruntime.Timeline](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+agent.ID+"/threads/"+main+"/events", origin, nil, 200)
	var interval []managedruntime.Event
	for _, e := range timeline.Events {
		if e.Sequence >= span.First && e.Sequence <= span.Last {
			interval = append(interval, e)
		}
	}
	if len(interval) != 3 || interval[0].Kind != "import.commit" || interval[2].Kind != "message.appended" {
		t.Fatal("Memory provenance cannot resolve the complete Runtime Commit", interval)
	}
	reviews := managementCall[memory.ReviewPage](t, f.client, "GET", base+"/reviews", origin, nil, 200)
	byID := map[string]memory.ReviewSummary{}
	for _, r := range reviews.Reviews {
		byID[r.ID] = r
	}
	r := byID["original-review"]
	if len(reviews.Reviews) != 3 || r.AgentID != agent.ID || r.ThreadID != main || r.State != "failed" || r.Attempts != 3 || r.WorkerID != "" || byID["retry-review"].State != "no_change" || byID["human-action"].AgentID != "" || byID["human-action"].ThreadID != "" || !byID["human-action"].Committed {
		t.Fatal("API fabricated a Worker or review success", reviews)
	}
	rules := managementCall[memory.StorageRules](t, f.client, "GET", base+"/storage-rules", origin, nil, 200)
	if len(rules.Entries) != 1 || rules.Entries[0] != "forgotten" || len(rules.Sources) != 1 || rules.Sources[0].GenerationID != "" || rules.Sources[0].Through != uint64(runtime.CommitSpans["0"][4].Last) {
		t.Fatal("API lost cross-Generation suppression", rules)
	}
	managementCall[memory.Status](t, f.client, "PUT", base, origin, managementhttp.MemoryConfiguration{Version: 1, Enabled: true, Strategy: mc.Advanced}, 200)
	if err := memorypg.New(f.pool).ImportFleet(ctx, owner, value); err != nil {
		t.Fatal("exact retry changed after new configuration", err)
	}
	if err := svc.Repository.View(ctx, owner, func(s *memory.State) error {
		if s.Control.Version != 2 || len(s.ImportedSources[agent.ID+"/"+main].Evidence) != 1 || len(s.Participation) != 0 || s.Keys["automatic/"+agent.ID+"/"+main+"/"+work.Proposal.Key] != "retry-review" || len(s.Admin) != 0 {
			t.Fatal("retry/configuration discarded history or activated it")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var noticesAfter, attempts, jobs int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.notification_outbox),(SELECT count(*) FROM runtime.attempts),(SELECT count(*) FROM runtime.application_jobs)`).Scan(&noticesAfter, &attempts, &jobs); err != nil || noticesAfter != noticesBefore || attempts != 0 || jobs != 0 || effects.calls.Load() != 0 {
		t.Fatal("import replayed work or notifications", noticesBefore, noticesAfter, attempts, jobs, effects.calls.Load(), err)
	}
	target := lifecycle.Target{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, FleetID: owner.FleetID, AgentIDs: []string{agent.ID}}
	if _, err := memorypg.New(f.pool).Purge(ctx, lifecycle.Request{Target: target, Phase: lifecycle.Erase}); err != nil {
		t.Fatal(err)
	}
	reviews = managementCall[memory.ReviewPage](t, f.client, "GET", base+"/reviews", origin, nil, 200)
	if len(reviews.Reviews) != 1 || reviews.Reviews[0].ID != "human-action" {
		t.Fatal("Agent cleanup removed Fleet human history", reviews)
	}
	target.ID, target.WholeFleet = uuid.NewString(), true
	if _, err := memorypg.New(f.pool).Purge(ctx, lifecycle.Request{Target: target, Phase: lifecycle.Erase}); err != nil {
		t.Fatal(err)
	}
	var fleets, imports int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM memory.fleets),(SELECT count(*) FROM memory.imports)`).Scan(&fleets, &imports); err != nil || fleets != 0 || imports != 0 {
		t.Fatal("Fleet cleanup retained history or import receipt", fleets, imports, err)
	}
}
