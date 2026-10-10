package management

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
)

func TestWorkspaceConfigurationResolvesExplicitCatalogWithoutPrivateInput(t *testing.T) {
	models := []Model{{ID: "c31b08bf-7a0c-4c8b-9b02-d3124c10f7ab", Provider: "local", Name: "primary"}, {ID: "a54f2f66-7d1c-4d6d-8e42-4a09f30c8245", Provider: "remote", Name: "backup"}}
	value, err := ParseWorkspaceConfiguration([]byte("models: [remote:backup, local:primary]\nmodules:\n  mcp: false\n  memory: true\n"), models)
	if err != nil || !slices.Equal(value.Models, []string{models[1].ID, models[0].ID}) || value.Modules[agentpolicy.MCP] || !value.Modules[agentpolicy.Memory] {
		t.Fatal(value, err)
	}
	for _, input := range []string{
		"models: []", "models: [missing:model]", "models: [local:primary, " + models[0].ID + "]",
		"modules: {unknown: false}", "modules: {mcp: false, mcp: true}", "modules: {mcp: null}",
		"providers: {private: {api_key: secret}}", "models: [${MODEL}]", "modules: {mcp: false}\n---\nmodules: {mcp: true}",
		"#" + strings.Repeat("x", (64<<10)+1), "#\xff",
	} {
		if _, err := ParseWorkspaceConfiguration([]byte(input), models); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid workspace configuration accepted: %q: %v", input[:min(len(input), 100)], err)
		}
	}
	inherited, err := ParseWorkspaceConfiguration([]byte("modules: {mcp: false}"), models)
	if err != nil || inherited.Models != nil {
		t.Fatal("missing models must inherit", inherited, err)
	}
}
