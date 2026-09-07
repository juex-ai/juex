package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModuleInspectionConfigUsesLayeredSelectionWithoutRuntimeSetup(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	work := t.TempDir()
	agent := filepath.Join(t.TempDir(), "juex.yaml")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(userHome, ".juex", "juex.yaml"), "preset: minimal\nmodules:\n  goal:\n    enabled: true\n")
	write(filepath.Join(home, "juex.yaml"), "modules:\n  notes:\n    enabled: true\n")
	write(filepath.Join(work, ".juex", "juex.yaml"), "imports:\n  - source: ./shared.yaml\nmodules:\n  notes:\n    enabled: false\n")
	write(filepath.Join(work, ".juex", "shared.yaml"), "modules:\n  scratchpad:\n    enabled: true\n")
	write(agent, "modules:\n  goal:\n    enabled: false\n")
	cfg, err := ReadModuleInspectionConfig(work, home, agent)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EffectivePreset() != PresetMinimal || cfg.ModuleEnabled("goal") || cfg.ModuleEnabled("notes") || !cfg.ModuleEnabled("scratchpad") {
		t.Fatalf("composition=%+v", cfg.Modules)
	}
	if _, err := os.Stat(filepath.Join(home, "cache")); !os.IsNotExist(err) {
		t.Fatalf("inspection created cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents")); !os.IsNotExist(err) {
		t.Fatalf("inspection created Agent state: %v", err)
	}
}

func TestModuleInspectionConfigReadsValidatedRemoteContentWithoutFetchingOrRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	work := t.TempDir()
	source := filepath.Join(home, "juex.yaml")
	remote := "https://unreachable.invalid/module-config.yaml"
	if err := os.WriteFile(source, []byte("imports:\n  - source: "+remote+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := newConfigImportLoader(home)
	loader.contextDigest = configImportContextDigest(work)
	content := "preset: minimal\nmodules:\n  notes:\n    enabled: true\n"
	record := configImportCacheRecord{Version: configImportCacheVersion, Source: remote, SourceSHA256: sourceDigest(remote), DeclaringSHA256: declaringConfigDigest(source), ContextSHA256: loader.contextDigest, FetchedAt: time.Now(), Content: content, ContentSHA256: contentDigest([]byte(content))}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := loader.cachePath(remote, source)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadModuleInspectionConfig(work, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModuleEnabled("goal") || !cfg.ModuleEnabled("notes") {
		t.Fatalf("composition=%+v", cfg.Modules)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(data) {
		t.Fatal("inspection changed validated import")
	}
	journal := configImportCacheJournalPath(home)
	if err := os.WriteFile(journal, []byte("pending publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadModuleInspectionConfig(work, home, ""); err == nil {
		t.Fatal("pending publication must be unavailable")
	}
	if after, _ := os.ReadFile(journal); string(after) != "pending publication" {
		t.Fatal("inspection recovered journal")
	}
}
