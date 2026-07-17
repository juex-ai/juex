package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateWorkspaceConfigReplacesOldWorkspaceLayerWithoutIdentity(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	configPath := filepath.Join(workDir, ".juex", "juex.yaml")
	writeTextFile(t, configPath, "unknown_old_field: true\n")
	candidate := []byte(`model: local:new-model
providers:
  - id: local
    protocol: openai/chat
    base_url: http://127.0.0.1:12345
    api_key: test-key
    models:
      - id: new-model
`)

	cfg, err := ValidateWorkspaceConfig(candidate, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "local" || cfg.Model != "new-model" {
		t.Fatalf("validated selection = %s:%s", cfg.ProviderID, cfg.Model)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".juex", "juex.local.json")); !os.IsNotExist(err) {
		t.Fatalf("validation created workspace identity: %v", err)
	}
}

func TestWriteWorkspaceConfigPreservesOldFileOnValidationFailure(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	configPath := filepath.Join(workDir, ".juex", "juex.yaml")
	old := []byte("model: existing:model\n")
	writeTextFile(t, configPath, string(old))

	if _, err := WriteWorkspaceConfig([]byte("unknown_field: true\n"), workDir); err == nil {
		t.Fatal("WriteWorkspaceConfig accepted an unknown field")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("config changed after failed validation:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".juex", "juex.local.json")); !os.IsNotExist(err) {
		t.Fatalf("failed write created workspace identity: %v", err)
	}
}

func TestWriteWorkspaceConfigPublishesValidatedCandidate(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	candidate := []byte(`model: local:new-model
providers:
  - id: local
    protocol: openai/chat
    base_url: http://127.0.0.1:12345
    api_key: test-key
    models:
      - id: new-model
`)

	path, err := WriteWorkspaceConfig(candidate, workDir)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(workDir, ".juex", "juex.yaml")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(candidate) {
		t.Fatalf("config = %q, want %q", got, candidate)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}
