package app

import (
	"context"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetManagementToolsOnlySupervisorMain(t *testing.T) {
	for _, test := range []struct {
		profile, id string
		want        bool
	}{{"supervisor", thread.MainID, true}, {"agent", thread.MainID, false}, {"supervisor", "worker-123", false}} {
		t.Run(test.profile+"/"+test.id, func(t *testing.T) {
			cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), Preset: "standard", FleetClientProfile: test.profile, HomeJuexDir: t.TempDir()}
			found := false
			for _, spec := range threadFactorySpecs(cfg, nil, &thread.Thread{ID: test.id}, nil, t.TempDir(), threadModuleOptions{}, nil) {
				if spec.ID != "fleet-management" {
					continue
				}
				found = true
				if spec.Enabled != test.want {
					t.Fatalf("enabled=%v want=%v", spec.Enabled, test.want)
				}
				if !spec.Enabled {
					continue
				}
				module, err := spec.New(context.Background(), runtimemodule.ThreadContext{})
				if err != nil {
					t.Fatal(err)
				}
				provider := module.(runtimemodule.ToolProvider)
				tools, err := provider.Tools(context.Background(), runtimemodule.ToolContext{})
				if err != nil || len(tools) != 5 {
					t.Fatalf("offline tools=%d %v", len(tools), err)
				}
			}
			if !found {
				t.Fatal("management composition missing")
			}
		})
	}
}

func TestSupervisorWorkerRuntimeDoesNotInheritManagement(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir, AgentStateDir: filepath.Join(dir, "state"), HomeJuexDir: t.TempDir(), FleetClientProfile: "supervisor"}
	main, err := New(Options{Config: cfg, Provider: &stubProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := New(Options{Config: cfg, Provider: &stubProvider{}, parentThreadID: thread.MainID, disableObservables: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close() }()
	count := func(app *App) int {
		n := 0
		for _, tool := range app.Engine.Tools.List() {
			if strings.HasPrefix(tool.Name, "fleet_") {
				n++
			}
		}
		return n
	}
	if count(main) != 5 || count(worker) != 0 {
		t.Fatalf("Main tools=%d Worker tools=%d", count(main), count(worker))
	}
}
