package config

import (
	"fmt"
	"testing"
)

func TestWorkerDepthConfiguration(t *testing.T) {
	for _, preset := range []string{PresetMinimal, PresetStandard} {
		cfg := Config{ModuleInventory: testModuleInventory(), Preset: preset}
		if cfg.WorkerMaxDepth() != 1 {
			t.Fatal("default depth must be 1")
		}
		for _, layer := range []string{
			"modules:\n  worker-threads:\n    enabled: false\n    max_depth: 2\n",
			"modules:\n  worker-threads:\n    enabled: true\n",
			"modules:\n  worker-threads:\n    max_depth: 1\n",
		} {
			if err := applyYAMLData(&cfg, []byte(layer), workspaceYAMLSource("depth.yaml")); err != nil {
				t.Fatal(err)
			}
		}
		if cfg.WorkerMaxDepth() != 1 || !cfg.ModuleEnabled("worker-threads") {
			t.Fatalf("independent depth/enablement merge failed: %+v", cfg)
		}
		cfg = Config{ModuleInventory: testModuleInventory(), Preset: preset}
		if err := applyYAMLData(&cfg, []byte("modules:\n  worker-threads:\n    max_depth: 2\n"), workspaceYAMLSource("depth.yaml")); err != nil {
			t.Fatal(err)
		}
		if cfg.WorkerMaxDepth() != 2 || cfg.ModuleEnabled("worker-threads") != (preset == PresetStandard) || len(cfg.Modules) != 0 {
			t.Fatal("depth override changed preset enablement")
		}
	}
}

func TestWorkerDepthRejectsInvalidYAML(t *testing.T) {
	for _, value := range []string{"0", "-1", "3", "1.5", "1.0", "null", "~", "", "true", "'2'", "[]", "{}"} {
		t.Run(value, func(t *testing.T) {
			cfg := Config{ModuleInventory: testModuleInventory()}
			input := fmt.Sprintf("modules:\n  worker-threads:\n    enabled: false\n    max_depth: %s\n", value)
			if err := applyYAMLData(&cfg, []byte(input), workspaceYAMLSource("invalid.yaml")); err == nil {
				t.Fatalf("accepted max_depth: %s", value)
			}
		})
	}
	for _, value := range []string{"1", "null"} {
		cfg := Config{ModuleInventory: testModuleInventory()}
		if err := applyYAMLData(&cfg, []byte("modules:\n  shell:\n    max_depth: "+value+"\n"), workspaceYAMLSource("invalid.yaml")); err == nil {
			t.Fatal("another module accepted max_depth")
		}
	}
}
