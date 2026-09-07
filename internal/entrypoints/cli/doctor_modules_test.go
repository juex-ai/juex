package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/mcp"
	skillsmodule "github.com/juex-ai/juex/internal/features/skills"
)

func TestDoctorDisabledResourcesSkipInspection(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	for _, path := range []string{".agents/mcp.json", ".agents/skills/broken/SKILL.md"} {
		full := filepath.Join(work, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("[broken: :"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, preset := range []string{config.PresetMinimal, config.PresetStandard} {
		t.Run(preset, func(t *testing.T) {
			cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, Preset: preset, Modules: config.ModulePolicy{
				skillsmodule.ModuleID: {Enabled: false}, mcp.ModuleID: {Enabled: false},
			}}
			checks := []doctorCheck{doctorSkillsCheck(cfg)}
			for _, offline := range []bool{false, true} {
				checks = append(checks, doctorMCPCheck(t.Context(), cfg, app.AgentRuntimeResolution{}, errors.New("unrelated enabled Extension failure"), offline))
				checks = append(checks, doctorMCPCheckWithAgentRuntimeOptions(t.Context(), cfg, app.AgentRuntimeResolution{}, mcp.RemoteReadinessOptions{Offline: offline}))
			}
			for _, check := range checks {
				if check.Status != doctorStatus("disabled") || check.Suggestion != "" {
					t.Errorf("disabled resource inspection = %+v", check)
				}
			}
			if worstDoctorStatus(checks) != doctorStatusOK {
				t.Fatal("disabled capabilities must not fail diagnose")
			}
		})
	}
}

func TestDoctorCommandReportsDisabledExecutionDependencies(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	path := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(path, "openai", "https://example.invalid", "sk-test", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, []byte("\npreset: minimal\nmodules:\n  shell:\n    enabled: false\n")...)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})
	_ = root.Execute()
	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	for _, name := range []string{"shell", "ripgrep", "mcp", "skills"} {
		found := false
		for _, check := range result.Checks {
			if check.Name == name {
				found = true
				if check.Status != doctorStatusDisabled {
					t.Errorf("%s = %+v", name, check)
				}
			}
		}
		if !found {
			t.Errorf("missing %s check", name)
		}
	}
}
