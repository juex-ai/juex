package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	web "github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type moduleEntryProvider struct{ calls atomic.Int32 }

func (*moduleEntryProvider) Name() string { return "module-entry" }

func (p *moduleEntryProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	p.calls.Add(1)
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "retained history"), StopReason: llm.StopEndTurn}, nil
}

func TestModuleEntriesKeepDisabledResourcesUnopened(t *testing.T) {
	for _, preset := range []string{config.PresetMinimal, config.PresetStandard} {
		t.Run(preset, func(t *testing.T) {
			root, work := isolateModuleConfig(t), t.TempDir()
			address, err := agentstate.Resolve(agentstate.Options{HomeDir: filepath.Join(root, ".juex"), WorkDir: work})
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, AgentStateDir: address.Address.StateDir(), AgentAddress: address.Address, Preset: preset,
				Modules:    config.ModulePolicy{"mcp": {Enabled: false}, "skills": {Enabled: false}, "extensions": {Enabled: false}, "observables": {Enabled: false}, "hooks": {Enabled: false}},
				Extensions: config.ExtensionPolicy{Configured: true, Allow: []string{"broken"}},
			}
			var probes atomic.Int32
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { probes.Add(1); w.WriteHeader(500) }))
			defer probe.Close()
			body, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"probe": map[string]string{"type": "http", "url": probe.URL}}})
			writeE2EConfig(t, filepath.Join(work, ".agents", "mcp.json"), string(body))
			writeE2EConfig(t, filepath.Join(work, ".agents", "skills"), "invalid directory")
			writeE2EConfig(t, filepath.Join(work, ".juex", "extensions", "broken", "juex.extension.json"), "{")
			writeE2EConfig(t, cfg.ObservablesConfigPath(), "{")
			previewContent := "preset: " + preset + "\nextensions:\n  allow: [broken]\nmodules:\n"
			for _, id := range []string{"mcp", "skills", "extensions", "observables", "hooks"} {
				previewContent += "  " + id + ":\n    enabled: false\n"
			}
			preview, err := config.ValidateAgentConfig(modulecatalog.Inventory(), []byte(previewContent), filepath.Join(root, ".juex"), address.Agent.ID)
			if err != nil {
				t.Fatalf("disabled resource config preview: %v", err)
			}
			if preview.ModuleEnabled("mcp") || preview.ModuleEnabled("skills") {
				t.Fatal("config preview disagrees with disabled catalog")
			}
			srv := web.NewServer(web.Options{Cfg: cfg, Provider: &moduleEntryProvider{}})
			defer srv.Close()
			handler := srv.APIHandler()
			response := moduleEntryRequest(t, handler, "GET", "/api/runtime", "", 200)
			var status struct {
				Modules []struct {
					ID string `json:"id"`
				} `json:"modules"`
				Extensions struct {
					Enabled bool `json:"enabled"`
					Count   int  `json:"count"`
				} `json:"extensions"`
				MCP struct {
					Configured int `json:"configured"`
				} `json:"mcp"`
				Skills struct {
					Count int `json:"count"`
				} `json:"skills"`
			}
			if err := json.Unmarshal([]byte(response), &status); err != nil {
				t.Fatal(err)
			}
			if status.Extensions.Enabled || status.Extensions.Count != 0 || status.MCP.Configured != 0 || status.Skills.Count != 0 {
				t.Fatalf("disabled resources: %s", response)
			}
			for _, module := range status.Modules {
				if setting, exists := cfg.Modules[module.ID]; exists && !setting.Enabled {
					t.Errorf("disabled module in active catalog: %s", module.ID)
				}
			}
			moduleEntryRequest(t, handler, "GET", "/api/observables", "", 403)
			moduleEntryRequest(t, handler, "POST", "/api/observables/hidden/run", "{}", 403)
			if probes.Load() != 0 {
				t.Fatalf("disabled MCP received %d probes", probes.Load())
			}
		})
	}
}

func TestWorkerDisabledAPIKeepsHistoryAndHostMaintenance(t *testing.T) {
	isolateModuleConfig(t)
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal,
		Modules: config.ModulePolicy{"worker-threads": {Enabled: true}},
	}
	if err := agent.EnsureMainThread(cfg.RuntimePaths().StateDir); err != nil {
		t.Fatal(err)
	}
	worker, err := thread.NewStore(cfg.AgentStateDir).CreateWorker("0", "retained")
	if err != nil {
		t.Fatal(err)
	}
	id := worker.ID
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := app.New(app.Options{Config: cfg, ThreadID: id, Provider: &moduleEntryProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Run(t.Context(), "write history"); err != nil {
		t.Fatal(err)
	}
	pending, err := writer.Engine.ReceivePendingInput(t.Context(), runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "retained pending"), DeferDelivery: true})
	if err != nil || pending.RecordID == "" {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	if err := writer.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	cfg.Modules["worker-threads"] = config.ModuleSettings{Enabled: false}
	provider := &moduleEntryProvider{}
	srv := web.NewServer(web.Options{Cfg: cfg, Provider: provider})
	defer srv.Close()
	handler := srv.APIHandler()
	base := "/api/threads/" + id
	if body := moduleEntryRequest(t, handler, "GET", base, "", 200); !strings.Contains(body, "retained history") {
		t.Fatalf("history missing: %s", body)
	}
	moduleEntryRequest(t, handler, "POST", base+"/inputs", `{"prompt":"hidden execution"}`, 403)
	moduleEntryRequest(t, handler, "GET", base+"/status", "", 200)
	moduleEntryRequest(t, handler, "POST", base+"/inputs", `{"prompt":"/status"}`, 200)
	if provider.calls.Load() != 0 {
		t.Fatal("opening Worker views recovered pending input")
	}
	moduleEntryRequest(t, handler, "POST", base+"/inputs", `{"prompt":"/new"}`, 200)
	moduleEntryRequest(t, handler, "POST", base+"/compact", `{}`, 200)
	if provider.calls.Load() != 0 {
		t.Fatal("host maintenance started Worker execution")
	}
	moduleEntryRequest(t, handler, "POST", base+"/archive", `{}`, 200)
	moduleEntryRequest(t, handler, "GET", base, "", 200)
	moduleEntryRequest(t, handler, "POST", base+"/unarchive", `{}`, 200)
	moduleEntryRequest(t, handler, "POST", base+"/archive", `{}`, 200)
	moduleEntryRequest(t, handler, "DELETE", base, "", 200)
}

func moduleEntryRequest(t *testing.T, handler http.Handler, method, path, body string, status int) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
	response := recorder.Result()
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, recorder.Code, status, result)
	}
	return string(result)
}
