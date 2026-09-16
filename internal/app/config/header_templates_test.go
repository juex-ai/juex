package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestHeaderTemplatesConfigImportsAndSave(t *testing.T) {
	prepareConfigTest(t)
	work, home := t.TempDir(), t.TempDir()
	resolution, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, filepath.Join(work, ".juex", "import.yaml"), `providers:
  - id: local
    protocol: openai/chat
    headers:
      X-Inherited: '${juex_agent_id}'
      X-Override: '${juex_generation_id}'
    models:
      - id: model
        headers:
          X-Override: '${juex_context_scope_id}'
models: [local:model]
`)
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), "imports:\n  - source: import.yaml\n")
	content := []byte("providers:\n  - id: local\n    headers:\n      X-Added: '${juex_thread_id}'\n")
	path, err := WriteAgentConfig(testModuleInventory(), content, home, resolution.Agent.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(content) {
		t.Fatalf("saved = %q, error = %v", raw, err)
	}
	cfg, err := LoadWithOptions(LoadOptions{ModuleInventory: testModuleInventory(), HomeDir: home, AgentID: resolution.Agent.ID, AgentState: AgentStateExisting})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"X-Inherited": "${juex_agent_id}", "X-Override": "${juex_context_scope_id}", "X-Added": "${juex_thread_id}"} {
		if cfg.ProviderHeaders[name] != want {
			t.Fatalf("header %s = %q", name, cfg.ProviderHeaders[name])
		}
	}
	for _, bad := range []string{"${juex_typo}", "${juex_agent_id"} {
		// The invalid model is not selected; save still validates its declaration.
		invalid := []byte("providers:\n  - id: local\n    models:\n      - id: unused\n        headers:\n          Authorization: 'secret-" + bad + "'\n")
		_, err = WriteAgentConfig(testModuleInventory(), invalid, home, resolution.Agent.ID, nil)
		if err == nil || !strings.Contains(err.Error(), "Authorization") || strings.Contains(err.Error(), "secret-") {
			t.Fatalf("validation error = %v", err)
		}
		raw, _ = os.ReadFile(path)
		if string(raw) != string(content) {
			t.Fatal("failed save overwrote original configuration")
		}
	}
}
