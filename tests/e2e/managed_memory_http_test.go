//go:build postgres

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
	memoryrpc "github.com/juex-ai/juex/internal/memory/rpc"
)

func TestManagedMemoryHumanHTTPAndIndependentSettings(t *testing.T) {
	ctx := context.Background()
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("human Memory HTTP invoked model") })
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	svc := &memory.Service{Repository: memorypg.New(f.pool), Authority: f.authority}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	memoryServer, err := serverrpc.NewMemory(listener, platformrpc.CredentialsAt(pki, "memory"), svc, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, memoryServer)
	memoryClient, err := memoryrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return memoryClient.Health(ctx) == nil })
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Memory: memoryClient, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	base := origin + "/api/tenants/" + f.tenant + "/users/" + f.actor + "/memory"
	status := managementCall[memory.Status](t, f.client, "GET", base, origin, nil, 200)
	if !status.Enabled || status.Strategy != mc.Basic {
		t.Fatal(status)
	}
	domains := managementCall[[]mc.Domain](t, f.client, "GET", base+"/domains", origin, nil, 200)
	if len(domains) == 0 || len(domains[0].Relations) != 0 {
		t.Fatal("domain summary lost", domains)
	}
	domain := managementCall[[]mc.Domain](t, f.client, "GET", base+"/domains?id=preferences", origin, nil, 200)
	if len(domain) != 1 || domain[0].ID != "preferences" || len(domain[0].Relations) == 0 {
		t.Fatal("domain relations missing", domain)
	}
	managementCall[any](t, f.client, "GET", base+"/domains?id=unknown", origin, nil, 400)
	managementCall[any](t, f.client, "GET", strings.Replace(base, "/users/"+f.actor, "/users/"+uuid.NewString(), 1)+"/domains", origin, nil, 403)
	managementCall[any](t, f.client, "GET", strings.Replace(base, f.tenant, uuid.NewString(), 1), origin, nil, 403)
	managementCall[any](t, f.client, "GET", strings.Replace(base, "/users/"+f.actor, "/users/"+uuid.NewString(), 1), origin, nil, 403)
	managementCall[any](t, f.client, "PUT", base, origin, map[string]any{"version": status.Version, "enabled": true, "strategy": mc.Basic, "agent_id": f.agent.ID}, 400)
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture := memoryFixture{service: svc, scope: scope, thread: f.main.ID, human: application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor}}
	binding, proposal := fixture.propose(t, "human-http")
	decision := memoryDecision("http-entry", proposal)
	decision.Changes[0].Entry.Entities = []mc.Entity{{ID: "user", Kind: "person", Name: "User"}}
	decision.Changes[0].Entry.Facts = []mc.Fact{{ID: "original", Domain: "preferences", Subject: "user", Predicate: "prefers", Value: "concise replies", Status: "valid", Reason: "User requested concise replies", Sources: proposal.Sources, SourceType: "user_statement", RecordedAt: proposal.Evidence[0].RecordedAt}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	decision.Changes[0].Entry.Facts[0].ValidFrom = &start
	if _, err := svc.Decide(ctx, scope, binding, decision, "http-decide"); err != nil {
		t.Fatal(err)
	}
	page := managementCall[mc.Page](t, f.client, "GET", base+"/entries?text=concise", origin, nil, 200)
	if len(page.Entries) != 1 || len(page.Entries[0].Sources) != 0 {
		t.Fatal(page)
	}
	entry := managementCall[mc.Entry](t, f.client, "GET", base+"/entries/http-entry?view=history", origin, nil, 200)
	if len(entry.Sources) != 1 {
		t.Fatal(entry)
	}
	stored := managementCall[mc.Entry](t, f.client, "GET", base+"/entries/http-entry?view=stored", origin, nil, 200)
	if stored.Revision != entry.Revision || stored.Name != "Response preference" {
		t.Fatal(stored)
	}
	if _, err := svc.Read(ctx, scope.Access, mc.ReadRequest{ID: entry.ID, View: "stored"}); err == nil {
		t.Fatal("Agent bypassed temporal projection with human editing view")
	}
	managementCall[mc.FactPage](t, f.client, "GET", base+"/facts?view=history", origin, nil, 200)
	reviews := managementCall[memory.ReviewPage](t, f.client, "GET", base+"/reviews", origin, nil, 200)
	if len(reviews.Reviews) != 1 || !reviews.Reviews[0].Committed {
		t.Fatal(reviews)
	}
	managementCall[any](t, f.client, "GET", base+"/reviews?offset=-1", origin, nil, 400)
	managementCall[any](t, f.client, "GET", base+"/entries?at=not-a-date", origin, nil, 400)
	entry.Body = "Explicit human correction"
	entry.Summary = "Corrected response preference"
	replacement := entry.Facts[0]
	replacement.ID, replacement.Value, replacement.Reason = "replacement", "two concise points", "Explicit human correction"
	replacement.RecordedAt, replacement.Replaces = time.Now().UTC(), []string{entry.Facts[0].ID}
	entry.Facts[0].Status = "corrected"
	entry.Facts = append(entry.Facts, replacement)
	request := mc.AdminRequest{Key: "human-correction", Action: "correct", Changes: []mc.Change{{Entry: entry, ExpectedRevision: entry.Revision}}}
	first := managementCall[mc.Receipt](t, f.client, "POST", base+"/administer", origin, request, 200)
	if replay := managementCall[mc.Receipt](t, f.client, "POST", base+"/administer", origin, request, 200); replay.ID != first.ID {
		t.Fatal("human correction duplicated")
	}
	current := managementCall[mc.FactPage](t, f.client, "GET", base+"/facts", origin, nil, 200)
	if len(current.Facts) != 1 || current.Facts[0].Fact.Value != "two concise points" {
		t.Fatal("human correction did not replace the current assertion", current)
	}
	for _, item := range []struct {
		query string
		count int
		value string
	}{
		{"domain=preferences&entity=user&predicate=prefers&view=current&status=current", 1, "two concise points"},
		{"domain=preferences&subject=user&predicate=prefers&view=history&status=corrected", 1, "concise replies"},
		{"domain=projects&entity=user&view=history", 0, ""},
		{"domain=preferences&entity=another-user&view=history", 0, ""},
		{"domain=preferences&view=as_of&at=2025-12-31T00:00:00Z", 0, ""},
		{"domain=preferences&view=as_of&at=2026-06-01T00:00:00Z", 1, "two concise points"},
		{"domain=preferences&view=history&limit=1&offset=1", 1, "two concise points"},
	} {
		page := managementCall[mc.FactPage](t, f.client, "GET", base+"/facts?"+item.query, origin, nil, 200)
		if len(page.Facts) != item.count || (item.count > 0 && page.Facts[0].Fact.Value != item.value) {
			t.Fatalf("wrong temporal or metadata projection for %s: %+v", item.query, page)
		}
	}
	request.Key = "stale-correction"
	managementCall[any](t, f.client, "POST", base+"/administer", origin, request, 409)
	status = managementCall[memory.Status](t, f.client, "PUT", base, origin, managementhttp.MemoryConfiguration{Version: status.Version, Enabled: false, Strategy: mc.Basic}, 200)
	if status.Enabled {
		t.Fatal(status)
	}
	managementCall[[]mc.Domain](t, f.client, "GET", base+"/domains?id=preferences", origin, nil, 200)
	if page := managementCall[mc.Page](t, f.client, "GET", base+"/entries", origin, nil, 200); len(page.Entries) != 1 {
		t.Fatal("disabled human read", page)
	}
	managementCall[any](t, f.client, "POST", base+"/administer", origin, mc.AdminRequest{Key: "disabled-delete", Action: "delete", EntryIDs: []string{entry.ID}}, 409)
	status = managementCall[memory.Status](t, f.client, "PUT", base, origin, managementhttp.MemoryConfiguration{Version: status.Version, Enabled: true, Strategy: mc.Basic}, 200)
	if !status.Enabled || status.Epoch != 3 {
		t.Fatal(status)
	}
	managementCall[mc.Receipt](t, f.client, "POST", base+"/administer", origin, mc.AdminRequest{Key: "forget", Action: "delete", EntryIDs: []string{entry.ID}}, 200)
	rules := managementCall[memory.StorageRules](t, f.client, "GET", base+"/storage-rules?limit=1", origin, nil, 200)
	if len(rules.Entries) != 1 || len(rules.Sources) != 1 {
		t.Fatal(rules)
	}
	managementCall[mc.Receipt](t, f.client, "POST", base+"/administer", origin, mc.AdminRequest{Key: "relearn", Action: "allow_store", EntryIDs: rules.Entries, Sources: rules.Sources}, 200)
	if rules := managementCall[memory.StorageRules](t, f.client, "GET", base+"/storage-rules", origin, nil, 200); len(rules.Entries)+len(rules.Sources) != 0 {
		t.Fatal(rules)
	}
}
