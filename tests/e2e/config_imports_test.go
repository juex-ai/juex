package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
)

func TestConfigImportsAcrossHomeWorkspaceAndExplicitLayers(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	home := filepath.Join(userHome, ".juex")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUEX_HOME", home)
	for _, key := range []string{
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
	} {
		t.Setenv(key, "")
	}

	writeE2EConfig(t, filepath.Join(home, "provider-base.yaml"), `models: [local:base]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://base.invalid
    api_key: test-key
    headers: {X-Home: home}
    models:
      - id: base
`)
	writeE2EConfig(t, filepath.Join(home, "juex.yaml"), `imports:
  - source: provider-base.yaml
runtime:
  tool_timeout: 20s
`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`providers:
  - id: local
    headers: {X-Workspace: workspace}
    models:
      - id: workspace
environment:
  variables: {IMPORTED_LAYER: workspace}
runtime:
  tool_timeout: 30s
`))
	}))
	defer server.Close()
	workDir := t.TempDir()
	writeE2EConfig(t, filepath.Join(workDir, ".juex", "juex.yaml"), `imports:
  - source: `+server.URL+`/workspace.yaml
models: [local:workspace]
runtime:
  tool_timeout: 40s
`)

	explicitDir := t.TempDir()
	writeE2EConfig(t, filepath.Join(explicitDir, "model.yaml"), `providers:
  - id: local
    models:
      - id: final
runtime:
  tool_timeout: 50s
`)
	explicitPath := filepath.Join(explicitDir, "juex.yaml")
	writeE2EConfig(t, explicitPath, `imports:
  - source: model.yaml
models: [local:final]
runtime:
  tool_timeout: 60s
`)

	cfg, err := config.LoadFromFileForWorkDirForValidation(explicitPath, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "local" || cfg.Model != "final" || cfg.ToolTimeout != 60*time.Second {
		t.Fatalf("effective config = provider:%q model:%q timeout:%s", cfg.ProviderID, cfg.Model, cfg.ToolTimeout)
	}
	if cfg.ProviderHeaders["X-Home"] != "home" || cfg.ProviderHeaders["X-Workspace"] != "workspace" {
		t.Fatalf("provider headers = %+v, want keyed map merge across imports", cfg.ProviderHeaders)
	}
	if value, ok := cfg.EnvironmentSnapshot().Lookup("IMPORTED_LAYER"); !ok || value != "workspace" {
		t.Fatalf("IMPORTED_LAYER = %q, %v", value, ok)
	}
	metadata := cfg.EnvironmentSnapshot().ConfiguredMetadata()
	if len(metadata) != 1 || metadata[0].Source != environment.SourceWorkspaceConfig || metadata[0].Path != server.URL+"/workspace.yaml" {
		t.Fatalf("environment metadata = %+v", metadata)
	}
	statuses := cfg.ImportStatuses()
	if len(statuses) != 3 || statuses[0].State != "fresh" || statuses[1].State != "fresh" || statuses[2].State != "fresh" {
		t.Fatalf("import statuses = %+v", statuses)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".juex", "juex.local.json")); !os.IsNotExist(err) {
		t.Fatalf("validation created agent state: %v", err)
	}
}

func writeE2EConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
