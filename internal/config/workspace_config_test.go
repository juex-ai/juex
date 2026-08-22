package config

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
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
	workDir := t.TempDir()
	cfg, err := ValidateWorkspaceConfig(candidate, workDir)
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
	if _, err := os.Stat(filepath.Join(homeDir, "cache", "config-imports")); !os.IsNotExist(err) {
		t.Fatalf("validation published remote cache directory: %v", err)
	}
	lock, err := homestore.AcquireLock(configImportCacheLockPath(homeDir), homestore.LockTry)
	if err != nil {
		t.Fatalf("successful validation left the import cache lock held: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWorkspaceConfigReleasesImportCacheLockOnCandidateFailure(t *testing.T) {
	prepareConfigTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("runtime:\n  tool_timeout: 44s\n"))
	}))
	defer server.Close()

	candidate := []byte("imports:\n  - source: " + server.URL + "/shared.yaml\nunknown_field: true\n")
	if _, err := ValidateWorkspaceConfig(candidate, t.TempDir()); err == nil {
		t.Fatal("ValidateWorkspaceConfig() accepted an unknown candidate field")
	}
	homeDir, err := EffectiveHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := homestore.AcquireLock(configImportCacheLockPath(homeDir), homestore.LockTry)
	if err != nil {
		t.Fatalf("candidate failure left the import cache lock held: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
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
		if _, err := os.Stat(filepath.Join(homeDir, "cache", "config-imports")); !os.IsNotExist(err) {
			t.Fatalf("failed workspace write published remote cache directory: %v", err)
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
		workDir := t.TempDir()
		path, err := WriteWorkspaceConfig(candidate, workDir)
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
		entries, err := os.ReadDir(filepath.Join(homeDir, "cache", "config-imports"))
		if err != nil {
			t.Fatalf("successful workspace write did not publish remote cache: %v", err)
		}
		if len(entries) != 1 || entries[0].IsDir() {
			t.Fatalf("remote cache entries = %+v, want one file", entries)
		}
	})
}

func TestWriteWorkspaceConfigRollsBackWorkspaceWhenImportCachePublicationFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  []byte
	}{
		{name: "existing workspace", old: []byte("runtime:\n  tool_timeout: 40s\n")},
		{name: "new workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareConfigTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("runtime:\n  tool_timeout: 44s\n"))
			}))
			defer server.Close()

			workDir := t.TempDir()
			configPath := filepath.Join(workDir, ".juex", "juex.yaml")
			if tc.old != nil {
				writeTextFile(t, configPath, string(tc.old))
			}
			candidate := []byte("imports:\n  - source: " + server.URL + "/shared.yaml\n")
			publishErr := errors.New("injected cache publication failure")
			_, err := writeWorkspaceConfig(candidate, workDir, func(cfg *Config) error {
				if len(cfg.pendingImportCache) != 1 {
					t.Fatalf("pending import cache records = %d, want 1", len(cfg.pendingImportCache))
				}
				unexpectedLock, lockErr := homestore.AcquireLock(configImportCacheLockPath(cfg.HomeJuexDir), homestore.LockTry)
				if lockErr == nil {
					_ = unexpectedLock.Close()
					t.Fatal("workspace config became visible before the import cache lock was held")
				}
				if !errors.Is(lockErr, homestore.ErrLockBusy) {
					t.Fatalf("probe import cache lock: %v", lockErr)
				}
				return publishErr
			})
			if !errors.Is(err, publishErr) {
				t.Fatalf("writeWorkspaceConfig() error = %v, want injected publication failure", err)
			}

			got, readErr := os.ReadFile(configPath)
			if tc.old == nil {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("new workspace config survived cache failure: data=%q err=%v", got, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != string(tc.old) {
				t.Fatalf("workspace config after cache failure = %q, want %q", got, tc.old)
			}
		})
	}
}

func TestWriteWorkspaceConfigRetainsImportCacheLockForStaleCandidate(t *testing.T) {
	prepareConfigTest(t)
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if unavailable.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("runtime:\n  tool_timeout: 44s\n"))
	}))
	defer server.Close()

	workDir := t.TempDir()
	candidate := []byte("imports:\n  - source: " + server.URL + "/shared.yaml\n")
	if _, err := WriteWorkspaceConfig(candidate, workDir); err != nil {
		t.Fatal(err)
	}
	homeDir, err := EffectiveHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cacheReader := newConfigImportLoader(homeDir)
	cacheReader.contextDigest = configImportContextDigest(workDir)
	if _, err := cacheReader.readCache(server.URL+"/shared.yaml", filepath.Join(workDir, ".juex", "juex.yaml")); err != nil {
		entries, _ := os.ReadDir(filepath.Join(homeDir, "cache", "config-imports"))
		t.Fatalf("seeded workspace cache is unreadable: %v entries=%v", err, entries)
	}
	if err := cacheReader.closeConfigImportCacheLock(); err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)

	if _, err := writeWorkspaceConfig(candidate, workDir, func(cfg *Config) error {
		if len(cfg.pendingImportCache) != 0 {
			t.Fatalf("stale candidate pending cache records = %d, want 0", len(cfg.pendingImportCache))
		}
		probe, lockErr := homestore.AcquireLock(configImportCacheLockPath(cfg.HomeJuexDir), homestore.LockTry)
		if probe != nil {
			_ = probe.Close()
		}
		if !errors.Is(lockErr, homestore.ErrLockBusy) {
			t.Fatalf("stale candidate import cache lock probe = %v, want ErrLockBusy", lockErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceConfigRecoversInterruptedPublicationBeforeLoad(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  []byte
	}{
		{name: "existing workspace", old: []byte("runtime:\n  tool_timeout: 40s\n")},
		{name: "new workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepareConfigTest(t)
			workDir := t.TempDir()
			path := filepath.Join(workDir, ".juex", "juex.yaml")
			candidate := []byte("runtime:\n  tool_timeout: 41s\n")
			if tc.old != nil {
				writeTextFile(t, path, string(tc.old))
			}
			snapshot, err := snapshotWorkspaceConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			homeDir, err := EffectiveHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := beginConfigImportCachePublicationWithWorkspace(homeDir, nil, &snapshot); err != nil {
				t.Fatal(err)
			}
			if err := homestore.WriteFileAtomic(path, candidate, 0o600, 0o755); err != nil {
				t.Fatal(err)
			}

			cfg, err := loadConfigFilesForWorkDir(workDir)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.importLoader == nil {
				t.Fatal("config load did not retain its import loader")
			}
			if err := cfg.importLoader.closeConfigImportCacheLock(); err != nil {
				t.Fatal(err)
			}
			got, readErr := os.ReadFile(path)
			if tc.old == nil {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("new workspace survived interrupted publication: data=%q err=%v", got, readErr)
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if cfg.ToolTimeout != 40*time.Second {
					t.Fatalf("loaded tool timeout = %s, want recovered 40s", cfg.ToolTimeout)
				}
				if string(got) != string(tc.old) {
					t.Fatalf("workspace after recovery = %q, want %q", got, tc.old)
				}
			}
			if _, err := os.Stat(configImportCacheJournalPath(homeDir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("workspace publication journal remains after recovery: %v", err)
			}
		})
	}
}
