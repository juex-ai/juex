package config

import (
	"strings"
	"testing"
)

func TestModuleInventoryIsExplicitAndImmutable(t *testing.T) {
	source := []ModuleDefinition{{ID: "base", Minimal: true}, {ID: "optional"}}
	inventory := NewModuleInventory(source)
	source[0].ID = "mutated"
	snapshot := inventory.Definitions()
	snapshot[1].ID = "mutated"
	cfg := Config{ModuleInventory: inventory, Preset: PresetMinimal}
	if !cfg.ModuleEnabled("base") || cfg.ModuleEnabled("optional") || cfg.ModuleEnabled("mutated") {
		t.Fatal("module policy did not retain its declaration inventory")
	}
	if err := (Config{}).ValidateModules(); err == nil || !strings.Contains(err.Error(), "inventory") {
		t.Fatalf("missing inventory error = %v", err)
	}
	if (Config{Modules: ModulePolicy{"base": {Enabled: true}}}).ModuleEnabled("base") {
		t.Fatal("an explicit switch enabled an undeclared module")
	}
}
