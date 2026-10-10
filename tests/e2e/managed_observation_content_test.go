//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagedObservationContentIsBoundedScopedAndIndependentOfHistoryWindow(t *testing.T) {
	x := executionDatabase(t)
	f := x.managedRuntimeFixture
	ctx := context.Background()
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(x.credentials, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(x.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(nil)
	f.origin = "http://" + web.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Runtime: client, PublicURL: f.origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	web.Config.Handler = handler
	web.Start()
	t.Cleanup(web.Close)
	managementCall[management.Session](t, f.client, "POST", f.origin+"/api/auth/login", f.origin, map[string]string{"email": "runtime@example.test", "password": "runtime test password"}, 200)
	f.base = f.origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
	id, environment := uuid.NewString(), uuid.NewString()
	data, _ := json.Marshal(map[string]any{"full_content": strings.Repeat("观察结果\n", 30000), "attachments": []map[string]string{{"artifact_id": uuid.NewString(), "name": "proof.png"}}})
	if _, err := f.pool.Exec(ctx, `INSERT INTO runtime.observations(event_id,agent_id,kind,environment_id,operation_id,source_offset,data,created_at) VALUES($1,$2,'command.observation',$3,'original-operation',73,$4,$5)`, id, f.agent.ID, environment, data, time.Now()); err != nil {
		t.Fatal(err)
	}
	first := managementCall[managedruntime.ObservationContent](t, f.client, http.MethodGet, f.base+"/observations/"+id+"?limit=17", f.origin, nil, http.StatusOK)
	if !first.HasMore || first.NextOffset != 17 || len([]rune(first.Data)) != 17 || first.EnvironmentID != environment || first.OperationID != "original-operation" {
		t.Fatal(first)
	}
	var joined strings.Builder
	joined.WriteString(first.Data)
	for next := first.NextOffset; next < first.TotalCharacters; {
		part := managementCall[managedruntime.ObservationContent](t, f.client, http.MethodGet, f.base+"/observations/"+id+"?offset="+strconv.Itoa(next)+"&limit=65536", f.origin, nil, http.StatusOK)
		if part.NextOffset <= next || part.ID != id || part.TotalCharacters != first.TotalCharacters {
			t.Fatal(part)
		}
		joined.WriteString(part.Data)
		next = part.NextOffset
	}
	var original, restored any
	if json.Unmarshal(data, &original) != nil || json.Unmarshal([]byte(joined.String()), &restored) != nil {
		t.Fatal("invalid paged JSON")
	}
	expected, _ := json.Marshal(original)
	actual, _ := json.Marshal(restored)
	if string(expected) != string(actual) {
		t.Fatal("content changed at Unicode boundary")
	}
	for _, query := range []string{"?limit=65537", "?offset=-1", "?offset=bad", "?offset=9999999"} {
		managementCall[map[string]any](t, f.client, http.MethodGet, f.base+"/observations/"+id+query, f.origin, nil, http.StatusBadRequest)
	}
	managementCall[map[string]any](t, f.client, http.MethodGet, f.base+"/observations/"+uuid.NewString(), f.origin, nil, http.StatusForbidden)
	var sequence int64
	if err := f.pool.QueryRow(ctx, `SELECT sequence FROM runtime.threads WHERE id=$1`, f.main.ID).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if sequence != f.main.Sequence {
		t.Fatal("read initialized Runtime work", sequence)
	}
	secondID := uuid.NewString()
	if _, err := f.pool.Exec(ctx, `INSERT INTO runtime.observations(event_id,agent_id,kind,environment_id,operation_id,source_offset,data,created_at) VALUES($1,$2,'command.observation',$3,'second-operation',74,'{"full_content":"second original","attachments":[{"name":"second.png","artifact_id":"00000000-0000-4000-8000-000000000002"}]}',clock_timestamp())`, secondID, f.agent.ID, environment); err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	input, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "observation-origin", Text: "External observation data"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, scope.AgentID, "origin", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.BeginTurn(ctx, lease, scope, input.ID, config); err != nil {
		t.Fatal(err)
	}
	page := managementCall[managedruntime.Timeline](t, f.client, http.MethodGet, f.base+"/threads/"+f.main.ID+"/events?limit=200", f.origin, nil, http.StatusOK)
	var batchSequence int64
	for _, event := range page.Events {
		if len(event.ObservationIDs) == 2 && slices.Contains(event.ObservationIDs, id) && slices.Contains(event.ObservationIDs, secondID) {
			batchSequence = event.Sequence
		}
	}
	if batchSequence == 0 {
		t.Fatal("Main automatic observation batch lost origins", page)
	}
	history := managementCall[managedruntime.Timeline](t, f.client, http.MethodGet, f.base+"/threads/"+f.main.ID+"/events?limit=1&before="+strconv.FormatInt(batchSequence+1, 10), f.origin, nil, http.StatusOK)
	if len(history.Events) != 1 || !slices.Equal(history.Events[0].ObservationIDs, []string{id, secondID}) {
		t.Fatal("history batch lost source order", history)
	}
	// Seed a retained delivered notice; the source process and subscription are
	// intentionally absent so reads cannot depend on their current lifecycle.
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.inputs SET source=$2 WHERE id=$1`, input.ID, managedruntime.InputSource{Kind: "observation", ObservationID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.events SET data=jsonb_set(data,'{kind}','"system_notice"') WHERE thread_id=$1 AND kind='message.appended' AND data->>'id'=$2`, f.main.ID, input.ID); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/events?after=2&limit=200", "/events?before=5&limit=1"} {
		page := managementCall[managedruntime.Timeline](t, f.client, http.MethodGet, f.base+"/threads/"+f.main.ID+suffix, f.origin, nil, http.StatusOK)
		found := false
		for _, event := range page.Events {
			if event.Kind == "message.appended" {
				found = found || slices.Contains(event.ObservationIDs, id)
			}
		}
		if !found {
			t.Fatal("window lost owner observation identity", suffix, page)
		}
	}
	peer, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other observation owner"})
	if err != nil {
		t.Fatal(err)
	}
	peerURL := f.origin + "/api/tenants/" + f.tenant + "/agents/" + peer.ID
	managementCall[map[string]any](t, f.client, http.MethodGet, peerURL+"/observations/"+id, f.origin, nil, http.StatusForbidden)
	admin, err := f.directory.CreateUser(ctx, "observation-admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, admin.Email, management.Admin, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.AcceptInvitation(ctx, admin.ID, token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.ChangeMember(ctx, admin.ID, f.tenant, f.actor, management.Admin, management.Suspended); err != nil {
		t.Fatal(err)
	}
	managementCall[map[string]any](t, f.client, http.MethodGet, f.base+"/observations/"+id+"?offset=17", f.origin, nil, http.StatusForbidden)
}
