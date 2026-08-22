package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestValidateWorkspaceConfigReplacesOldWorkspaceLayerWithoutIdentity(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	configPath := filepath.Join(workDir, ".juex", "juex.yaml")
	writeTextFile(t, configPath, "unknown_old_field: true\n")
	candidate := []byte(`models: [local:new-model]
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
	old := []byte("models: [existing:model]\n")
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
	candidate := []byte(`models: [local:new-model]
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestValidateAndWriteWorkspaceConfigResolveCandidateImports(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	importPath := filepath.Join(workDir, ".juex", "shared.yaml")
	writeTextFile(t, importPath, "runtime:\n  tool_timeout: 44s\n")
	candidate := []byte("imports:\n  - source: shared.yaml\n")

	cfg, err := ValidateWorkspaceConfig(candidate, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolTimeout != 44*time.Second {
		t.Fatalf("tool timeout = %s, want imported 44s", cfg.ToolTimeout)
	}
	path, err := WriteWorkspaceConfig(candidate, workDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(candidate) {
		t.Fatalf("written candidate = %q, want imports preserved verbatim", got)
	}
}

func TestValidateWorkspaceConfigDoesNotPublishRemoteImportCache(t *testing.T) {
	prepareConfigTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("runtime:\n  tool_timeout: 44s\n"))
	}))
	defer server.Close()

	source := server.URL + "/shared.yaml"
	candidate := []byte("imports:\n  - source: " + source + "\n")
	cfg, err := ValidateWorkspaceConfig(candidate, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolTimeout != 44*time.Second {
		t.Fatalf("tool timeout = %s, want imported 44s", cfg.ToolTimeout)
	}
	homeDir, err := EffectiveHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(homeDir, "cache", "config-imports", sourceDigest(source)+".json")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("validation published remote cache: %v", err)
	}
}

func TestWriteWorkspaceConfigPublishesRemoteImportCacheOnlyAfterWriteSucceeds(t *testing.T) {
	t.Run("write failure", func(t *testing.T) {
		prepareConfigTest(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 44s\n"))
		}))
		defer server.Close()

		source := server.URL + "/shared.yaml"
		workDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(workDir, ".juex"), []byte("not a directory\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate := []byte("imports:\n  - source: " + source + "\n")
		if _, err := WriteWorkspaceConfig(candidate, workDir); err == nil {
			t.Fatal("WriteWorkspaceConfig succeeded with a file blocking the workspace config directory")
		}
		homeDir, err := EffectiveHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		cachePath := filepath.Join(homeDir, "cache", "config-imports", sourceDigest(source)+".json")
		if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
			t.Fatalf("failed workspace write published remote cache: %v", err)
		}
	})

	t.Run("successful write", func(t *testing.T) {
		prepareConfigTest(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 44s\n"))
		}))
		defer server.Close()

		source := server.URL + "/shared.yaml"
		candidate := []byte("imports:\n  - source: " + source + "\n")
		path, err := WriteWorkspaceConfig(candidate, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("workspace config was not written: %v", err)
		}
		homeDir, err := EffectiveHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		cachePath := filepath.Join(homeDir, "cache", "config-imports", sourceDigest(source)+".json")
		if _, err := os.Stat(cachePath); err != nil {
			t.Fatalf("successful workspace write did not publish remote cache: %v", err)
		}
	})
}
