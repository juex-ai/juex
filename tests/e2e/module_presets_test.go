package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"
	"github.com/juex-ai/juex/internal/framework/agent"
)

func TestModulePresetsSharePolicyAcrossReadOnlyMainAndWorker(t *testing.T) {
	isolateModuleConfig(t)
	work := t.TempDir()
	configPath := filepath.Join(work, "preset.yaml")
	data := []byte("preset: minimal\nmodules:\n  shell:\n    enabled: false\n  basic-file-tools:\n    enabled: false\n  operating-context:\n    enabled: false\n  worker-threads:\n    enabled: true\n  notes:\n    enabled: true\n")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, HomeDir: t.TempDir(), ConfigPath: configPath, AgentState: config.AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ValidateModuleConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.AgentStateDir = filepath.Join(work, "state")
	main, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := main.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	create, ok := main.Engine.Tools.Get(workerthreadsmodule.ToolCreate)
	if !ok {
		t.Fatal("Worker tool unavailable")
	}
	result, err := create.Handler(context.Background(), map[string]any{"query": "reply done", "alias": "preset-worker"})
	if err != nil {
		t.Fatal(err)
	}
	var status agent.WorkerThreadStatus
	if err := json.Unmarshal([]byte(result), &status); err != nil {
		t.Fatal(err)
	}
	worker, ok := main.ManagedWorkerAgent(status.ThreadID)
	if !ok {
		t.Fatalf("Worker not managed: %s", result)
	}
	for name, application := range map[string]*agent.Agent{"Main": main.Agent, "Worker": worker} {
		for module, tool := range map[string]string{"goal": "get_goal", "notes": "update_notes", "context-control": "context_new", "skills": "skill_search", "basic-file-tools": "read", "worker-threads": "thread_create"} {
			_, available := application.Engine.Tools.Get(tool)
			if available != cfg.ModuleEnabled(module) {
				t.Errorf("%s %s availability = %v, configuration = %v", name, tool, available, cfg.ModuleEnabled(module))
			}
		}
		if err := app.ReadRuntimeModuleSnapshot(application, func(active app.RuntimeModuleSnapshot) error {
			observed, err := app.NewRuntimeCatalogService(cfg).Snapshot(app.RuntimeStatusOptions{ActiveModules: &active})
			if err != nil {
				return err
			}
			if observed.Tools.Count != len(application.Engine.Tools.List()) {
				t.Errorf("%s read-only catalog count = %d", name, observed.Tools.Count)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func isolateModuleConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("JUEX_HOME", filepath.Join(root, ".juex"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "missing-codex-home"))
	for _, key := range []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW"} {
		t.Setenv(key, "")
	}
	return root
}
