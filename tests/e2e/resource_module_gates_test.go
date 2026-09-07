package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestResourceModuleGatesBeforeAppStartup(t *testing.T) {
	for _, tc := range []struct{ module, source string }{
		{"hooks", "extension"}, {"mcp", "extension"}, {"skills", "extension"}, {"observables", "extension"},
		{"hooks", "workspace"}, {"mcp", "workspace"}, {"skills", "workspace"}, {"observables", "agent"},
	} {
		t.Run(tc.module+"/"+tc.source, func(t *testing.T) {
			root, work := isolateModuleConfig(t), t.TempDir()
			home := filepath.Join(root, ".juex")
			address, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
			if err != nil {
				t.Fatal(err)
			}
			extDir := filepath.Join(work, ".juex", "extensions", "fixture")
			writeE2EConfig(t, filepath.Join(extDir, "juex.extension.json"), `{"manifest_version":1,"name":"fixture","version":"1.0.0"}`)
			workspaceYAML := "preset: minimal\nmodules:\n  extensions:\n    enabled: true\nextensions:\n  allow: [fixture]\n"
			resourceDir := extDir
			if tc.source == "workspace" {
				resourceDir = filepath.Join(work, ".agents")
			}
			if tc.source == "agent" {
				resourceDir = address.Address.StateDir()
			}
			var filename, body string
			switch tc.module {
			case "hooks":
				filename, body = "hooks.yaml", "commands: broken\n"
				if tc.source == "workspace" {
					workspaceYAML += "hooks: broken\n"
					filename = ""
				}
			case "mcp":
				filename, body = "mcp.json", "{"
			case "skills":
				filename, body = "skills", "retained invalid directory"
			case "observables":
				filename, body = "observables.json", "{"
			}
			writeE2EConfig(t, filepath.Join(work, ".juex", "juex.yaml"), workspaceYAML)
			if filename != "" {
				writeE2EConfig(t, filepath.Join(resourceDir, filename), body)
			}
			off := []byte("modules:\n  " + tc.module + ":\n    enabled: false\n")
			if _, err := config.WriteAgentConfig(off, home, address.Agent.ID, app.ValidateModuleConfig); err != nil {
				t.Fatal(err)
			}
			load := func() config.Config {
				t.Helper()
				cfg, err := config.LoadWithOptions(config.LoadOptions{HomeDir: home, AgentID: address.Agent.ID, AgentState: config.AgentStateExisting})
				if err != nil {
					t.Fatal(err)
				}
				return cfg
			}
			cfg := load()
			for cycle := 0; cycle < 2; cycle++ {
				application, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}}}})
				if err != nil {
					t.Fatalf("disabled startup %d: %v", cycle, err)
				}
				if _, err := application.Engine.Turn(t.Context(), "reply done"); err != nil {
					t.Error(err)
				}
				if err := application.CloseAndWait(); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{filepath.Join(address.Address.StateDir(), "extensions"), cfg.ObservablesStateDir()} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("disabled resource prepared %s: %v", path, err)
					}
				}
			}
			// Re-enable through configuration for embedded declarations, or through
			// the actual App consumer for resources validated at startup.
			on := []byte("modules:\n  " + tc.module + ":\n    enabled: true\n")
			if tc.module == "hooks" && tc.source == "workspace" {
				if _, err := config.ValidateAgentConfig(on, home, address.Agent.ID); err == nil {
					t.Fatal("enabled hook declaration accepted")
				}
				return
			}
			if _, err := config.WriteAgentConfig(on, home, address.Agent.ID, app.ValidateModuleConfig); err != nil {
				t.Fatal(err)
			}
			application, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
			if tc.module == "observables" {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = application.CloseAndWait() })
				statuses := application.Observables().Status().Observables
				if len(statuses) != 1 || statuses[0].LastError == "" {
					t.Fatalf("enabled invalid observable diagnostic = %+v", statuses)
				}
				return
			}
			if err == nil {
				_ = application.CloseAndWait()
				t.Fatal("enabled resource accepted invalid content")
			}
		})
	}
}

func TestExtensionSwitchPreservesWorkspaceMCPExecution(t *testing.T) {
	remote := newWebRemoteMCPServer(t)
	work, home := t.TempDir(), t.TempDir()
	writeE2EConfig(t, filepath.Join(work, ".agents", "mcp.json"), fmt.Sprintf(`{"mcpServers":{"remote":{"type":"http","url":%q}}}`, remote.URL))
	writeE2EConfig(t, filepath.Join(work, ".juex", "extensions", "broken", "juex.extension.json"), "{")
	cfg := config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: home,
		Extensions: config.ExtensionPolicy{Allow: []string{"broken"}, Configured: true},
		Modules:    config.ModulePolicy{"mcp": {Enabled: true}},
	}
	application, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })
	echo, ok := application.Engine.Tools.Get("mcp__remote__echo")
	if !ok {
		t.Fatal("workspace MCP tool absent")
	}
	result, err := echo.Handler(context.Background(), map[string]any{"text": "resource gate"})
	if err != nil || !strings.Contains(result, "remote: resource gate") {
		t.Fatalf("MCP result = %q, %v", result, err)
	}
}

func TestDisabledMCPDoesNotRequireResourceEnvironment(t *testing.T) {
	for _, extension := range []bool{false, true} {
		t.Run(fmt.Sprintf("extension=%v", extension), func(t *testing.T) {
			work, state := t.TempDir(), t.TempDir()
			dir := filepath.Join(work, ".agents")
			if extension {
				dir = filepath.Join(work, ".juex", "extensions", "fixture")
				writeE2EConfig(t, filepath.Join(dir, "juex.extension.json"), `{"manifest_version":1,"name":"fixture","version":"1.0.0"}`)
			}
			writeE2EConfig(t, filepath.Join(dir, "mcp.json"), `{"mcpServers":{"remote":{"type":"http","url":"https://mcp.example.invalid","headers":{"Authorization":"Bearer ${MISSING_MODULE_GATE_TOKEN}"}}}}`)
			cfg := config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: state,
				Extensions: config.ExtensionPolicy{Allow: []string{"fixture"}, Configured: true},
				Modules:    config.ModulePolicy{"extensions": {Enabled: true}},
			}
			application, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}})
			if err != nil {
				t.Fatalf("disabled MCP expanded environment: %v", err)
			}
			if err := application.CloseAndWait(); err != nil {
				t.Fatal(err)
			}
			cfg.Modules["mcp"] = config.ModuleSettings{Enabled: true}
			application, err = app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}})
			if err == nil {
				_ = application.CloseAndWait()
				t.Fatal("enabled MCP accepted missing environment")
			}
			if !strings.Contains(err.Error(), "MISSING_MODULE_GATE_TOKEN") {
				t.Fatalf("enabled MCP error = %v", err)
			}
		})
	}
}
