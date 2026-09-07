package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/framework/runtime/workmem"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/internal/entrypoints/agenthttp"
)

func TestWeb_ModuleInspectionAcrossRetentionAndComposition(t *testing.T) {
	cfg := config.Config{AgentID: "abcdef", WorkDir: t.TempDir(), AgentStateDir: t.TempDir()}
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
	worker, err := store.CreateWorker("0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := worker.Close(); err != nil {
			t.Error(err)
		}
	}()
	goals := workmem.NewGoalStateStore(worker.Dir, workmem.GoalStateOptions{})
	if _, err := goals.Create("read without runtime", "preserve all files"); err != nil {
		t.Fatal(err)
	}
	notes := workmem.NewNotesStore(worker.Dir)
	if _, err := notes.Update("retained notes"); err != nil {
		t.Fatal(err)
	}
	for _, archived := range []bool{false, true} {
		if archived {
			if err := store.Archive(worker); err != nil {
				t.Fatal(err)
			}
		}
		for _, enabled := range []bool{true, false} {
			cfg.Modules = config.ModulePolicy{"goal": {Enabled: enabled}, "notes": {Enabled: enabled}, "scratchpad": {Enabled: enabled}}
			before := moduleInspectionDisk(t, cfg.AgentStateDir)
			// Fleet's stopped-Agent adapter must provide the same snapshot contract.
			server := httptest.NewServer(web.NewReadOnlyAPIHandler(cfg))
			base := server.URL + "/api/threads/" + worker.ID + "/modules"
			response, err := http.Get(base)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 {
				body, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				t.Fatalf("snapshot %d: %s", response.StatusCode, body)
			}
			var snapshot web.ThreadModulesSnapshot
			if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if !snapshot.ReadOnly || snapshot.AgentID != cfg.AgentID || snapshot.ThreadID != worker.ID {
				t.Fatalf("scope=%+v", snapshot)
			}
			if enabled {
				if !strings.Contains(string(snapshot.Modules["goal"].Value), "read without runtime") || !strings.Contains(string(snapshot.Modules["notes"].Value), "retained notes") || len(snapshot.UI) != 3 {
					t.Fatalf("enabled=%+v", snapshot)
				}
			} else if len(snapshot.Modules) != 0 || len(snapshot.UI) != 0 {
				t.Fatalf("disabled=%+v", snapshot)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			req, _ := http.NewRequestWithContext(ctx, "GET", base+"/events", nil)
			req.Header.Set("Last-Event-ID", "another-agent:999")
			response, err = server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(response.Body)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(line, "data: ") {
					var streamed web.ThreadModulesSnapshot
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &streamed); err != nil {
						t.Fatal(err)
					}
					if streamed.Revision != snapshot.Revision {
						t.Fatalf("baseline differs from GET: %+v", streamed)
					}
					break
				}
			}
			completion, err := io.ReadAll(reader)
			if err != nil || !strings.Contains(string(completion), "event: revalidate\n") {
				t.Fatalf("read-only baseline must signal revalidation before closing: %q, %v", completion, err)
			}
			_ = response.Body.Close()
			cancel()
			server.Close()
			if after := moduleInspectionDisk(t, cfg.AgentStateDir); after != before {
				t.Fatalf("inspection changed disk: archived=%v enabled=%v", archived, enabled)
			}
		}
	}
}

func moduleInspectionDisk(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out.WriteString(path)
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}
