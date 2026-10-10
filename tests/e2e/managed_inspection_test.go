//go:build postgres

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedThreadInspection(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "Ready") })
	ctx := context.Background()
	endpoint := f.base + "/threads/" + f.main.ID + "/inspection"
	read := func() map[string]any {
		return managementCall[map[string]any](t, f.client, "GET", endpoint, f.origin, nil, 200)
	}
	initial := read()
	if initial["latest_request"] != nil || initial["thread"].(map[string]any)["sequence"] != float64(f.main.Sequence) {
		t.Fatalf("empty inspection changed runtime: %#v", initial)
	}
	state := managedruntime.ThreadState{Notes: managedruntime.ThreadNotes{Content: "Keep the user's requirement"}, Tasks: []managedruntime.ThreadTask{{ID: "task", Title: "Finish acceptance", Status: "doing", Priority: "high"}}, Revision: 4}
	if _, err := f.pool.Exec(ctx, `INSERT INTO runtime.thread_state(thread_id,value) VALUES($1,$2)`, f.main.ID, state); err != nil {
		t.Fatal(err)
	}
	current := read()["state"].(map[string]any)
	if current["revision"] != float64(4) || current["notes"].(map[string]any)["content"] != state.Notes.Content || len(current["tasks"].([]any)) != 1 {
		t.Fatal(current)
	}
	// Remove fixture Tasks before running: unfinished Tasks correctly block completion.
	if _, err := f.pool.Exec(ctx, `DELETE FROM runtime.thread_state WHERE thread_id=$1`, f.main.ID); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.submit(t, "inspection-input", f.main.ID, "Hello")
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	value := read()
	latest := value["latest_request"].(map[string]any)
	if latest["model"] != "test-model" || latest["context_window"] != float64(32768) || latest["estimated_tokens"].(float64) <= 0 || latest["system"] == "" {
		t.Fatal(latest)
	}
	usage := value["usage"].(map[string]any)
	if usage["reported"] != float64(1) || usage["input_tokens"] != float64(12) || usage["output_tokens"] != float64(4) {
		t.Fatal(usage)
	}
	managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/reset-context", f.origin, map[string]string{"request_id": "inspection-reset"}, 200)
	if read()["latest_request"] != nil {
		t.Fatal("previous generation request presented as current")
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Unstarted"})
	if err != nil {
		t.Fatal(err)
	}
	otherBase := f.origin + "/api/tenants/" + f.tenant + "/agents/" + other.ID
	managementCall[map[string]any](t, f.client, "GET", otherBase+"/threads/"+uuid.NewString()+"/inspection", f.origin, nil, 403)
	managementCall[map[string]any](t, f.client, "GET", otherBase+"/threads/"+f.main.ID+"/inspection", f.origin, nil, 403)
	var created bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.agents WHERE id=$1)`, other.ID).Scan(&created); err != nil || created {
		t.Fatalf("read initialized runtime: %v %v", created, err)
	}
}
