package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestLegacyConfigCaptureResolvesOfflineWithoutAmbientCredentials(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	for _, dir := range []string{filepath.Join(home, "agents/abc234"), filepath.Join(home, "cache/config-imports"), filepath.Join(work, ".juex")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
		files[name] = data
	}
	digest := func(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); http.Error(w, "must not fetch", 500) }))
	defer server.Close()
	url := server.URL + "/models?token=fixture-secret"
	write(filepath.Join(home, "fleet.json"), []byte(`{"id":"source-fleet"}`))
	definition, _ := json.Marshal(legacy.AgentDefinition{ID: "abc234", Name: "Original", Workspace: work, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	write(filepath.Join(home, "agents/abc234/agent.json"), definition)
	write(filepath.Join(home, "juex.yaml"), []byte(fmt.Sprintf("imports: [{source: %q}]\n", url)))
	write(filepath.Join(work, ".juex/juex.yaml"), []byte("preset: minimal\nmodels: [fixture:local:tag]\n"))
	content := "providers:\n  - id: fixture\n    protocol: openai/chat\n    api_key: captured-only-secret\n    models: [{id: 'local:tag', context_window: 8192}]\n"
	context, declaring, source := digest("work_dir="+work), digest(filepath.Join(home, "juex.yaml")), digest(url)
	cache, _ := json.Marshal(map[string]any{"version": 3, "source": server.URL + "/models", "source_sha256": source, "declaring_sha256": declaring, "context_sha256": context, "fetched_at": "2026-01-01T00:00:00Z", "content": content, "content_sha256": "sha256:" + digest(content)})
	write(filepath.Join(home, "cache/config-imports", source+"-"+declaring+"-"+context+".json"), cache)
	t.Setenv("PROVIDER_API_KEY", "ambient-must-not-win")
	t.Setenv("JUEX_HOME", filepath.Join(root, "must-not-create"))
	fleet, err := legacy.ReadFleet(home, home)
	if err != nil {
		t.Fatal(err)
	}
	evidence := migration.ConfigEvidence{Contexts: map[string]migration.ConfigContext{"abc234": {WorkingDirectory: work}}, Identities: map[string]string{}}
	for _, p := range []string{work, filepath.Join(home, "juex.yaml"), filepath.Join(home, "agents/abc234/juex.yaml"), filepath.Join(work, ".juex/juex.yaml")} {
		evidence.Identities[p] = p
	}
	resolved, err := migration.ResolveConfig(fleet, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || len(resolved[0].Models) != 1 || resolved[0].Models[0].Configuration.APIKey != "captured-only-secret" || resolved[0].Models[0].Configuration.Model != "local:tag" || resolved[0].Modules["memory"] || requests.Load() != 0 {
		t.Fatal("offline source behavior changed")
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret", "captured-only-secret", "ambient-must-not-win"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("report contains a credential")
		}
	}
	for name, before := range files {
		after, err := os.ReadFile(name)
		if err != nil || string(before) != string(after) {
			t.Fatal("source changed during capture/conversion")
		}
	}
	for _, p := range []string{filepath.Join(home, "agents/abc234/juex.yaml"), filepath.Join(root, "must-not-create")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal("offline conversion initialized missing source state")
		}
	}
}
