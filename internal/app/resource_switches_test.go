package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	hookconfig "github.com/juex-ai/juex/internal/features/hooks/config"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestDisabledExtensionsLeaveWorkspaceResourcesAvailable(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "broken", "juex.extension.json"), "{")
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{"mcpServers":{"workspace":{"command":"echo"}}}`)
	cfg := config.Config{WorkDir: work, Extensions: allowExtensions("broken"), Modules: config.ModulePolicy{"extensions": {Enabled: false}}}
	graph, err := ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		t.Fatalf("disabled extension parsed: %v", err)
	}
	if len(graph.Extensions()) != 0 {
		t.Fatalf("extensions = %+v", graph.Extensions())
	}
	if got := graph.MCPConfigs(); len(got) != 1 || got[0].Source != "project" {
		t.Fatalf("workspace MCP = %+v", got)
	}
	if got := graph.SkillDirs(); len(got) != 1 || got[0].Source != "project" {
		t.Fatalf("workspace skills = %+v", got)
	}
	if _, _, _, err := loadMCPConfigRefs(graph.MCPConfigs(), work, cfg.EnvironmentSnapshot()); err != nil {
		t.Fatal(err)
	}
	cfg.Modules["extensions"] = config.ModuleSettings{Enabled: true}
	if _, err := ResolveRuntimeResourceGraph(cfg); err == nil {
		t.Fatal("enabled extension accepted broken manifest")
	}
}

func TestExtensionDefaultsFollowSourceSwitchWithoutPreparingData(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, ".juex", "extensions", "demo")
	manifest := filepath.Join(dir, "juex.extension.json")
	mustWriteRuntimeStatusFile(t, manifest, `{"manifest_version":1,"name":"demo","version":"1.0.0","agent":{"environment":{"variables":{"MODULE_GATE_DATA":"${JUEX_EXT_DATA_DIR}"}}}}`)
	cfg := config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentAddress: address, Extensions: allowExtensions("demo"), Modules: config.ModulePolicy{"extensions": {Enabled: true}}}
	resolved, err := ResolveAgentRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := resolved.Environment().Lookup("MODULE_GATE_DATA"); !ok || value != filepath.Join(address.StateDir(), "extensions", "demo") {
		t.Fatalf("shared default = %q, %v", value, ok)
	}
	if _, err := os.Stat(address.StateDir()); !os.IsNotExist(err) {
		t.Fatalf("default evaluation prepared data: %v", err)
	}
	mustWriteRuntimeStatusFile(t, manifest, `{"manifest_version":1,"name":"demo","version":"1.0.0","agent":{"environment":{"variables":{"MODULE_GATE_DATA":"${MISSING_MODULE_GATE_ENV}"}}}}`)
	if _, err := ResolveAgentRuntime(cfg); err == nil {
		t.Fatal("enabled Extension accepted invalid shared default")
	}
	cfg.Modules["extensions"] = config.ModuleSettings{Enabled: false}
	resolved, err = ResolveAgentRuntime(cfg)
	if err != nil {
		t.Fatalf("disabled Extension evaluated defaults: %v", err)
	}
	if _, ok := resolved.Environment().Lookup("MODULE_GATE_DATA"); ok || len(resolved.EnvironmentDeclarations()) != 0 {
		t.Fatal("disabled Extension default published")
	}
}

func TestDisabledResourceHostsSkipDiscovery(t *testing.T) {
	for _, tc := range []struct {
		module, filename string
		kind             RuntimeResourceKind
	}{
		{"skills", "skills", RuntimeResourceSkillDir},
		{"hooks", "hooks.yaml", RuntimeResourceHookFile},
		{"mcp", "mcp.json", RuntimeResourceMCPConfig},
		{"observables", "observables.json", RuntimeResourceObservableConfig},
	} {
		t.Run(tc.module, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			extDir := filepath.Join(work, ".juex", "extensions", "demo")
			mustWriteRuntimeStatusFile(t, filepath.Join(extDir, "juex.extension.json"), `{"manifest_version":1,"name":"demo","version":"1.0.0"}`)
			if err := os.MkdirAll(extDir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(extDir, tc.filename)
			if err := os.Symlink(tc.filename, path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			cfg := config.Config{WorkDir: work, HomeAgentsDir: home, EnableUserAgentsResources: true, Extensions: allowExtensions("demo"), Modules: config.ModulePolicy{tc.module: {Enabled: false}}, Hooks: hookconfig.Config{Commands: []hookconfig.CommandHook{{Name: "bad"}}}}
			// The unrelated test hook must not invalidate the other enabled hosts.
			if tc.module != "hooks" {
				cfg.Hooks = hookconfig.Config{}
			}
			graph, err := ResolveRuntimeResourceGraph(cfg)
			if err != nil {
				t.Fatalf("disabled host discovered invalid path: %v", err)
			}
			for _, node := range graph.Nodes() {
				if node.Kind == tc.kind {
					t.Fatalf("disabled node = %+v", node)
				}
			}
			if len(graph.Extensions()) != 1 {
				t.Fatalf("source manifest lost: %+v", graph.Extensions())
			}
			if tc.module == "hooks" && len(graph.HooksConfig().Commands) != 0 {
				t.Fatalf("disabled hooks = %+v", graph.HooksConfig())
			}
			cfg.Modules[tc.module] = config.ModuleSettings{Enabled: true}
			if _, err := ResolveRuntimeResourceGraph(cfg); err == nil {
				t.Fatal("enabled host did not discover invalid path")
			}
		})
	}
}
