package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	web "github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestWeb_RetainedInputStatusAcrossModuleSwitchAndArchive(t *testing.T) {
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), AgentID: "abcdef", WorkDir: t.TempDir(), AgentStateDir: t.TempDir()}
	store := thread.NewStore(cfg.AgentStateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker("0", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close() }()
	original := llm.TextMessage(llm.RoleUser, "retained original input")
	original.ID = "msg-original"
	association := runtime.InputTrackedPayload{InputID: "input-original", MessageID: original.ID, ScopeID: worker.ContextScopeID()}
	if _, err := worker.AppendAssigned(original, events.Event{Type: runtime.InputTrackedType, Payload: association}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Append(llm.TextMessage(llm.RoleUser, "newest untracked message")); err != nil {
		t.Fatal(err)
	}
	if err := worker.AppendEvent(events.Event{Type: runtime.InputCheckedType, Payload: runtime.InputCheckedPayload{
		InputIDs: []string{association.InputID}, ScopeID: association.ScopeID, CheckedAt: time.Now().UTC(), MessageID: "answer", ToolUseID: "check",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, archived := range []bool{false, true} {
		if archived {
			if err := store.Archive(worker); err != nil {
				t.Fatal(err)
			}
		}
		for _, enabled := range []bool{true, false, true} {
			cfg.Modules = config.ModulePolicy{"input-tracking": {Enabled: enabled}}
			// Fleet uses this handler for stopped Agents; it must read the same facts
			// without loading inputs.json or admitting another Turn.
			before := moduleInspectionDisk(t, cfg.AgentStateDir)
			server := httptest.NewServer(web.NewReadOnlyAPIHandler(cfg))
			response, err := http.Get(server.URL + "/api/threads/" + worker.ID + "?limit=1&input_message_id=" + original.ID + "&input_message_id=unknown")
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Items         []thread.TimelineItem    `json:"items"`
				InputTracking *runtime.InputStatusPage `json:"input_tracking"`
			}
			err = json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			server.Close()
			if err != nil || response.StatusCode != http.StatusOK || len(body.Items) != 1 {
				t.Fatalf("status=%d body=%+v err=%v", response.StatusCode, body, err)
			}
			if enabled {
				if body.InputTracking == nil || len(body.InputTracking.Messages) != 1 || body.InputTracking.Messages[original.ID].CheckMessageID != "answer" {
					t.Fatalf("retained annotation missing: %+v", body.InputTracking)
				}
			} else if body.InputTracking != nil {
				t.Fatal("disabled handler published annotations")
			}
			if moduleInspectionDisk(t, cfg.AgentStateDir) != before {
				t.Fatal("inspection mutated Agent/Thread storage")
			}
		}
	}
}
