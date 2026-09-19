package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/fleethttp"
	"github.com/juex-ai/juex/internal/fleet"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func TestEndToEnd_FleetMemoryAdministration(t *testing.T) {
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	source := mc.Source{FleetID: user.FleetID, AgentID: "source", ThreadID: "0", GenerationID: "g000001", From: 1, Through: 1}
	entry := mc.Entry{ID: "release-notes", Name: "Release notes", Summary: "Keep notes concise", Type: "reference", Body: "Original text", Scope: mc.Scope{Workspace: "/project"}, Sources: []mc.Source{source}}
	entry.Entities = []mc.Entity{{ID: "project", Name: "Project", Kind: "project"}}
	entry.Facts = []mc.Fact{{Subject: "project", Predicate: "release-notes", Value: "concise", Status: "valid", SourceType: "user_statement", Sources: entry.Sources, RecordedAt: time.Now().UTC()}}
	if _, err := api.Admin(t.Context(), user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: entry}}}); err != nil {
		t.Fatal(err)
	}
	entry, err := api.Read(t.Context(), user, mc.ReadRequest{ID: entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := fleet.New(fleet.Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	web, err := fleethttp.New(fleethttp.Options{Manager: manager})
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Handler()
	request := func(method, path string, body any, status int) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(method, path, bytes.NewReader(data)))
		if r.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	request("GET", "/api/memory/status", nil, http.StatusOK)
	var page mc.Page
	if err := json.Unmarshal(request("GET", "/api/memory/entries?q=concise&limit=1", nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].ID != entry.ID || page.Next != -1 {
		t.Fatalf("search: %+v", page)
	}
	request("GET", "/api/memory/entries?offset=-1", nil, 400)
	request("GET", "/api/memory/entries/absent", nil, 404)
	request("GET", "/api/memory/entries/release:notes", nil, 400)
	request("POST", "/api/memory/entries", nil, 405)
	path := "/api/memory/entries/" + entry.ID
	request("GET", path, nil, 200)
	changed := entry
	changed.Name = "Updated notes"
	changed.Body = "Corrected text"
	payload := map[string]any{"key": "web-edit", "entry": changed, "expected_revision": entry.Revision}
	var first, retry mc.Receipt
	if err := json.Unmarshal(request("PUT", path, payload, 200), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request("PUT", path, payload, 200), &retry); err != nil {
		t.Fatal(err)
	}
	if first.ID != retry.ID || !retry.Committed {
		t.Fatalf("edit retry: %+v %+v", first, retry)
	}
	got, err := api.Read(t.Context(), user, mc.ReadRequest{ID: entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != entry.Revision+1 || got.Body != changed.Body || got.Name != changed.Name || got.Scope != entry.Scope || !reflect.DeepEqual(got.Sources, entry.Sources) || !got.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("correction: %+v", got)
	}
	if !reflect.DeepEqual(got.Entities, entry.Entities) || !reflect.DeepEqual(got.Facts, entry.Facts) {
		t.Fatal("correction changed structured knowledge")
	}
	invalid := got
	invalid.Name = ""
	request("PUT", path, map[string]any{"key": "invalid-edit", "entry": invalid, "expected_revision": got.Revision}, 422)
	request("DELETE", path, map[string]any{"key": "unconfirmed-delete", "expected_revision": got.Revision}, 400)
	payload["key"] = "stale-edit"
	request("PUT", path, payload, 409)
	payload["expected_revision"] = 0
	request("PUT", path, payload, 400)
	payload["expected_revision"] = got.Revision
	payload["caller"] = map[string]string{"profile": "user"}
	request("PUT", path, payload, 400)
	request("DELETE", path, map[string]any{"key": "stale-delete", "expected_revision": entry.Revision, "confirm": entry.ID}, 409)
	deletion := map[string]any{"key": "web-delete", "expected_revision": got.Revision, "confirm": entry.ID}
	if err := json.Unmarshal(request("DELETE", path, deletion, 200), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request("DELETE", path, deletion, 200), &retry); err != nil {
		t.Fatal(err)
	}
	if first.ID != retry.ID || !retry.Committed {
		t.Fatalf("delete retry: %+v %+v", first, retry)
	}
	request("GET", path, nil, 404)
	if _, err := api.Admin(t.Context(), user, mc.AdminRequest{Key: "recreate", Action: "correct", Changes: []mc.Change{{Entry: entry}}}); err == nil {
		t.Fatal("deleted knowledge was relearned without explicit permission")
	}
}

func TestEndToEnd_FleetMemoryUnavailable(t *testing.T) {
	manager, err := fleet.New(fleet.Options{HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	web, err := fleethttp.New(fleethttp.Options{Manager: manager})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	web.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/memory/status", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline Memory: %d %s", response.Code, response.Body)
	}
}
