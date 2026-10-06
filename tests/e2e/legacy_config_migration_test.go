package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/migration/legacy"
	"github.com/juex-ai/juex/internal/providers"
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
	var modelRequests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelRequests.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.URL.Query().Get("routing") != "private-route" || r.Header.Get("Authorization") != "Bearer captured-only-secret" || r.Header.Get("X-Thread") != "target-thread" || body["model"] != "local:tag" {
			t.Error("converted provider request lost its source route or target request identity")
		}
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if _, present := body[key]; present {
				t.Error("conversion invented an output cap")
			}
		}
		if body["stream"] != true {
			t.Error("source streaming capability changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"local:tag\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"verified\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer modelServer.Close()
	url := server.URL + "/models?token=fixture-secret"
	write(filepath.Join(home, "fleet.json"), []byte(`{"id":"source-fleet"}`))
	definition, _ := json.Marshal(legacy.AgentDefinition{ID: "abc234", Name: "Original", Workspace: work, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	write(filepath.Join(home, "agents/abc234/agent.json"), definition)
	write(filepath.Join(home, "juex.yaml"), []byte(fmt.Sprintf("imports: [{source: %q}]\n", url)))
	write(filepath.Join(work, ".juex/juex.yaml"), []byte("preset: minimal\nmodels: [fixture:local:tag]\n"))
	content := fmt.Sprintf("providers:\n  - id: fixture\n    protocol: openai/chat\n    base_url: %q\n    api_key: captured-only-secret\n    headers: {X-Thread: '${juex_thread_id}'}\n    query: {routing: private-route}\n    models: [{id: 'local:tag', context_window: 8192}]\n", modelServer.URL)
	contextHash, declaring, source := digest("work_dir="+work), digest(filepath.Join(home, "juex.yaml")), digest(url)
	cache, _ := json.Marshal(map[string]any{"version": 3, "source": server.URL + "/models", "source_sha256": source, "declaring_sha256": declaring, "context_sha256": contextHash, "fetched_at": "2026-01-01T00:00:00Z", "content": content, "content_sha256": "sha256:" + digest(content)})
	write(filepath.Join(home, "cache/config-imports", source+"-"+declaring+"-"+contextHash+".json"), cache)
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
	capture, captureHash, err := legacy.EncodeCapture(fleet)
	if err != nil {
		t.Fatal(err)
	}
	// Keep logical source identities while making every original path unavailable.
	// Import must use the same private cache bytes after an interruption.
	frozenRoot := root + "-frozen"
	if err := os.Rename(root, frozenRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(frozenRoot, root) })
	fleet, err = legacy.DecodeCapture(capture, captureHash)
	if err != nil {
		t.Fatal(err)
	}
	fromCapture, err := migration.ResolveConfig(fleet, evidence)
	if err != nil || !reflect.DeepEqual(fromCapture, resolved) {
		t.Fatalf("private capture changed source configuration: %v", err)
	}
	resolved = fromCapture
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
	environment := map[string]*string{}
	for _, key := range []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW"} {
		environment[key] = nil
	}
	models, err := migration.ResolveModels(resolved[0], migration.ModelEvidence{AgentID: "abc234", Environment: environment})
	if err != nil || modelRequests.Load() != 0 {
		t.Fatal("model conversion performed I/O or failed", err)
	}
	provider, err := providers.NewProvider(models.Models[0].Profile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = llm.CompleteWithOptions(context.Background(), provider, "fixture", nil, nil, llm.CompleteOptions{SingleAttempt: true, Identity: llm.RequestIdentity{ThreadID: "target-thread"}, MaxOutputTokens: models.Models[0].MaxOutputTokens})
	if err != nil || modelRequests.Load() != 1 || requests.Load() != 0 {
		t.Fatal("converted profile did not complete exactly one fixture request", err)
	}
	for name, before := range files {
		after, err := os.ReadFile(strings.Replace(name, root, frozenRoot, 1))
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
