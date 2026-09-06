package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
)

func TestModulePresets(t *testing.T) {
	ids := []string{"basic-file-tools", "shell", "apply-patch", "chunked-write", "file-search", "operating-context", "agents-md", "skills", "scratchpad", "goal", "notes", "memory", "context-control", "worker-threads", "observables", "mcp", "hooks", "extensions"}
	for _, tc := range []struct {
		name, yaml string
		minimal    bool
		overrides  map[string]bool
	}{
		{name: "absent"},
		{name: "standard", yaml: "preset: standard\n"},
		{name: "minimal", yaml: "preset: minimal\n", minimal: true},
		{name: "minimal overrides", yaml: "preset: minimal\nmodules:\n  goal:\n    enabled: true\n  shell:\n    enabled: false\n", minimal: true, overrides: map[string]bool{"goal": true, "shell": false}},
		{name: "standard override", yaml: "preset: standard\nmodules:\n  notes:\n    enabled: false\n", overrides: map[string]bool{"notes": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{}
			if tc.yaml != "" {
				if err := applyYAMLData(&cfg, []byte(tc.yaml), workspaceYAMLSource("test.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range ids {
				want := !tc.minimal || id == "basic-file-tools" || id == "shell" || id == "operating-context"
				if value, ok := tc.overrides[id]; ok {
					want = value
				}
				if got := cfg.ModuleEnabled(id); got != want {
					t.Errorf("%s enabled = %v, want %v", id, got, want)
				}
			}
			if len(cfg.Modules) != len(tc.overrides) {
				t.Fatalf("preset expanded into explicit modules: %+v", cfg.Modules)
			}
		})
	}
}

func TestModulePresetLayeringPreservesExplicitSwitches(t *testing.T) {
	cfg := Config{}
	layers := []string{
		"preset: standard\nmodules:\n  skills:\n    enabled: true\n  shell:\n    enabled: false\n",
		"preset: minimal\n",
		"modules:\n  goal:\n    enabled: true\n  skills: {}\n",
	}
	for _, layer := range layers {
		if err := applyYAMLData(&cfg, []byte(layer), workspaceYAMLSource("layer.yaml")); err != nil {
			t.Fatal(err)
		}
	}
	if !cfg.ModuleEnabled("skills") || cfg.ModuleEnabled("shell") || !cfg.ModuleEnabled("goal") || cfg.ModuleEnabled("mcp") {
		t.Fatalf("effective modules = %+v", cfg)
	}
	if err := applyYAMLData(&cfg, []byte("preset: standard\nmodules:\n  skills:\n    enabled: false\n"), workspaceYAMLSource("last.yaml")); err != nil {
		t.Fatal(err)
	}
	if cfg.ModuleEnabled("skills") || cfg.ModuleEnabled("shell") || !cfg.ModuleEnabled("mcp") {
		t.Fatalf("effective modules = %+v", cfg.Modules)
	}
}

func TestModulePresetValidation(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"unknown preset", "preset: tiny\n", "unsupported preset"},
		{"empty preset", "preset: ''\n", "unsupported preset"},
		{"unknown module", "modules:\n  typo:\n    enabled: true\n", "unsupported module"},
		{"unknown empty module", "modules:\n  typo: {}\n", "unsupported module"},
		{"unknown null module", "modules:\n  typo: null\n", "unsupported module"},
		{"unknown setting", "modules:\n  goal:\n    active: true\n", "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{}
			err := applyYAMLData(&cfg, []byte(tc.yaml), workspaceYAMLSource("invalid.yaml"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestModulePresetsAgentImportsAndSparseRoundTrip(t *testing.T) {
	userHome := prepareConfigTest(t)
	home, work := t.TempDir(), t.TempDir()
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "modules:\n  skills:\n    enabled: true\n  shell:\n    enabled: false\n")
	writeTextFile(t, filepath.Join(home, "juex.yaml"), "preset: standard\n")
	writeTextFile(t, filepath.Join(work, ".juex", "import.yaml"), "preset: minimal\nmodules:\n  notes:\n    enabled: true\n")
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), "imports:\n  - source: import.yaml\n")
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, filepath.Join(resolved.Address.StateDir(), "switches.yaml"), "modules:\n  notes:\n    enabled: false\n")
	content := []byte("imports:\n  - source: switches.yaml\npreset: minimal\nmodules:\n  goal:\n    enabled: true\n")
	validated, err := ValidateAgentConfig(content, home, resolved.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := WriteAgentConfig(content, home, resolved.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("sparse YAML changed: %q", got)
	}
	loaded, err := LoadWithOptions(LoadOptions{HomeDir: home, WorkDir: work, AgentState: AgentStateExisting})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(validated.Modules, loaded.Modules) {
		t.Fatalf("validation modules %+v != reload %+v", validated.Modules, loaded.Modules)
	}
	if !loaded.ModuleEnabled("goal") || !loaded.ModuleEnabled("skills") || loaded.ModuleEnabled("shell") || loaded.ModuleEnabled("notes") || loaded.ModuleEnabled("mcp") {
		t.Fatalf("wrong effective policy: %+v", loaded.Modules)
	}
	for _, invalid := range []string{"preset: typo\n", "modules:\n  typo: {}\n"} {
		if _, err := WriteAgentConfig([]byte(invalid), home, resolved.Agent.ID); err == nil {
			t.Fatalf("saved invalid configuration %q", invalid)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(content) {
			t.Fatalf("rejected write changed overlay: %q, %v", got, err)
		}
	}
}

func TestModulePresetDoesNotChangeCoreConfiguration(t *testing.T) {
	cfg := Config{Model: "unchanged", ProviderID: "provider", Compaction: DefaultCompactionConfig(), ToolOutput: DefaultToolOutputConfig()}
	before := cfg
	if err := applyYAMLData(&cfg, []byte("preset: minimal\n"), workspaceYAMLSource("preset.yaml")); err != nil {
		t.Fatal(err)
	}
	if cfg.Model != before.Model || cfg.ProviderID != before.ProviderID || !reflect.DeepEqual(cfg.Sandbox, before.Sandbox) || cfg.Compaction != before.Compaction || cfg.ToolOutput != before.ToolOutput {
		t.Fatal("preset modified core configuration")
	}
}

func TestModulePresetProgrammaticValidation(t *testing.T) {
	for _, cfg := range []Config{
		{Preset: "typo"},
		{Modules: ModulePolicy{"typo": {Enabled: true}}},
	} {
		if err := cfg.ValidateModules(); err == nil {
			t.Fatal("invalid programmatic configuration accepted")
		}
	}
	if (Config{}).ModuleEnabled("unknown-future-module") {
		t.Fatal("unknown module enabled by default")
	}
}
