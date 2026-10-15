package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	web "github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/provenance"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestWeb_RecordedRecitationIsPassiveForActiveAndArchivedThreads(t *testing.T) {
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
	message := llm.TextMessage(llm.RoleUser, "## Notes\nRecorded text")
	message.ID = "runtime-notes"
	message.Kind = llm.MessageKindRuntimeContext
	epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{History: []llm.Message{message}})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-recorded"
	if err := worker.AppendEvent(events.Event{Type: provenance.RequestEpochType, TurnID: "turn-1", Payload: provenance.RequestEpochPayload{Epoch: epoch}}); err != nil {
		t.Fatal(err)
	}
	// Current module files need not be readable; inspecting the recorded request
	// must not collect Context, repair inputs, or touch staged generation files.
	for _, name := range []string{"inputs.json", "notes.json"} {
		if err := os.WriteFile(filepath.Join(worker.Dir, name), []byte("{incomplete"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, archived := range []bool{false, true} {
		if archived {
			if err := store.Archive(worker); err != nil {
				t.Fatal(err)
			}
		}
		metadata, err := store.Inspect(worker.ID)
		if err != nil {
			t.Fatal(err)
		}
		staged := filepath.Join(metadata.Dir, "generations", "g999999.jsonl")
		if err := os.WriteFile(staged, []byte("staged"), 0600); err != nil {
			t.Fatal(err)
		}
		before := moduleInspectionDisk(t, cfg.AgentStateDir)
		server := httptest.NewServer(web.NewReadOnlyAPIHandler(cfg))
		for range 2 {
			response, err := http.Get(server.URL + "/api/threads/" + worker.ID + "/recitation")
			if err != nil {
				t.Fatal(err)
			}
			var snapshot runtime.RecitationSnapshot
			err = json.NewDecoder(response.Body).Decode(&snapshot)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != 200 || snapshot.EpochID != epoch.EpochID || len(snapshot.Fragments) != 1 || snapshot.Fragments[0].Text != "## Notes\nRecorded text" {
				t.Fatalf("archived=%v status=%d snapshot=%+v err=%v", archived, response.StatusCode, snapshot, err)
			}
		}
		response, err := http.Get(server.URL + "/api/threads/absent/recitation")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("missing Thread status %d", response.StatusCode)
		}
		server.Close()
		if after := moduleInspectionDisk(t, cfg.AgentStateDir); after != before {
			t.Fatal("Recitation inspection mutated stored state")
		}
		if err := os.Remove(staged); err != nil {
			t.Fatal(err)
		}
	}
}
