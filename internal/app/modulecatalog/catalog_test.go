package modulecatalog

import (
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
)

func TestProductPresetMembership(t *testing.T) {
	expected := map[string]bool{
		"basic-file-tools": true, "shell": true, "operating-context": true,
		"apply-patch": false, "chunked-write": false, "file-search": false,
		"agents-md": false, "skills": false, "scratchpad": false, "goal": false,
		"notes": false, "memory": false, "context-control": false, "worker-threads": false,
		"input-tracking": false,
		"observables":    false, "mcp": false, "hooks": false, "extensions": false,
	}
	definitions := Inventory().Definitions()
	if len(definitions) != len(expected) {
		t.Fatalf("inventory size = %d, want %d", len(definitions), len(expected))
	}
	minimal := config.Config{ModuleInventory: Inventory(), Preset: config.PresetMinimal}
	standard := config.Config{ModuleInventory: Inventory(), Preset: config.PresetStandard}
	for _, definition := range definitions {
		want, ok := expected[definition.ID]
		if !ok || minimal.ModuleEnabled(definition.ID) != want || !standard.ModuleEnabled(definition.ID) {
			t.Errorf("unexpected preset membership: %+v", definition)
		}
	}
}
