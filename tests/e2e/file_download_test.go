package e2e

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	web "github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestWeb_FileDownloadPreservesThreadScopeAndArchivedResources(t *testing.T) {
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), AgentID: "abcdef", WorkDir: t.TempDir(), AgentStateDir: t.TempDir()}
	store := thread.NewStore(cfg.AgentStateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := main.Close(); err != nil {
			t.Error(err)
		}
	}()
	worker, err := store.CreateWorker("0", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := worker.Close(); err != nil {
			t.Error(err)
		}
	}()
	expected := map[string][]byte{}
	for _, item := range []*thread.Thread{main, worker} {
		dir := filepath.Join(item.Dir, "scratchpad")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		body := bytes.Repeat([]byte(item.ID+"<script>source</script>"), 30000)
		if err := os.WriteFile(filepath.Join(dir, "same.html"), body, 0644); err != nil {
			t.Fatal(err)
		}
		expected[item.ID] = body
	}
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(web.NewReadOnlyAPIHandler(cfg))
	defer server.Close()
	for id, body := range expected {
		response, err := http.Get(server.URL + "/api/threads/" + id + "/modules/scratchpad/resources/files/raw?path=same.html&download=1")
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || !bytes.Equal(got, body) {
			t.Fatalf("Thread %s: status=%d bytes=%d", id, response.StatusCode, len(got))
		}
		if response.Header.Get("Content-Type") != "application/octet-stream" {
			t.Fatal(response.Header)
		}
	}
}
