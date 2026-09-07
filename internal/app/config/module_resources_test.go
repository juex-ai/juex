package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestDisabledModuleDeclarationsWaitForFinalLayers(t *testing.T) {
	for _, tc := range []struct{ name, module, declaration, want string }{
		{"hook structure", "hooks", "hooks: broken\n", "unmarshal"},
		{"hook field", "hooks", "hooks:\n  unknown: true\n", "unknown"},
		{"hook trust", "hooks", "hooks:\n  commands:\n    - name: untrusted\n      events: [Stop]\n      command: [echo]\n", "hooks.trusted"},
		{"skill structure", "skills", "skills: broken\n", "unmarshal"},
		{"skill field", "skills", "skills:\n  unknown: true\n", "unknown"},
		{"skill budget", "skills", "skills:\n  prompt_budget_chars: -1\n", "non-negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareConfigTest(t)
			home, work := t.TempDir(), t.TempDir()
			writeTextFile(t, filepath.Join(work, ".juex", "broken.yaml"), tc.declaration)
			writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), "imports:\n  - source: broken.yaml\n")
			resolution, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
			if err != nil {
				t.Fatal(err)
			}
			off := []byte("modules:\n  " + tc.module + ":\n    enabled: false\n")
			if _, err := ValidateAgentConfig(off, home, resolution.Agent.ID); err != nil {
				t.Fatalf("preview disabled: %v", err)
			}
			if _, err := WriteAgentConfig(off, home, resolution.Agent.ID, nil); err != nil {
				t.Fatalf("save disabled: %v", err)
			}
			cfg, err := LoadWithOptions(LoadOptions{HomeDir: home, AgentID: resolution.Agent.ID, AgentState: AgentStateExisting})
			if err != nil {
				t.Fatalf("load disabled: %v", err)
			}
			if cfg.ModuleEnabled(tc.module) || len(cfg.Hooks.Commands) != 0 {
				t.Fatalf("disabled declarations published: %+v", cfg.Hooks)
			}
			on := []byte("modules:\n  " + tc.module + ":\n    enabled: true\n")
			if _, err := WriteAgentConfig(on, home, resolution.Agent.ID, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reenable error = %v, want %s", err, tc.want)
			}
			got, err := os.ReadFile(filepath.Join(resolution.Address.StateDir(), "juex.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(off) {
				t.Fatalf("failed save published %q", got)
			}
		})
	}
}

func TestDisabledModulesStillValidateCommonYAML(t *testing.T) {
	for _, content := range []string{"hooks: [\n", "unknown: true\n", "skills: broken\nmodules:\n  unknown:\n    enabled: false\n"} {
		prepareConfigTest(t)
		work := t.TempDir()
		writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), "preset: minimal\n"+content)
		if _, err := LoadWithOptions(LoadOptions{WorkDir: work, AgentState: AgentStateNone}); err == nil {
			t.Fatalf("accepted invalid common YAML %q", content)
		}
	}
}

func TestModuleDeclarationsPreserveAliasesAndFinalizeOnce(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), `skills:
  include: &command [echo, hello]
hooks:
  trusted: true
  commands:
    - name: aliased-command
      events: [Stop]
      command: *command
modules:
  skills:
    enabled: false
`)
	cfg, err := LoadWithOptions(LoadOptions{WorkDir: work, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := finalizeLoadedConfig(&cfg, false, false); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.Commands) != 1 || strings.Join(cfg.Hooks.Commands[0].Command, " ") != "echo hello" {
		t.Fatalf("resolved hooks = %+v", cfg.Hooks)
	}
}
