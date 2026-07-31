//go:build integration

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLiveProviderConfigPath(t *testing.T) {
	repo := t.TempDir()
	home := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "provider.yaml")

	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{
			name: "default",
			want: filepath.Join(home, ".juex", "juex.yaml"),
		},
		{
			name:       "absolute override",
			configured: absolute,
			want:       absolute,
		},
		{
			name:       "repo relative override",
			configured: filepath.Join("testdata", "provider.yaml"),
			want:       filepath.Join(repo, "testdata", "provider.yaml"),
		},
		{
			name:       "home relative override",
			configured: filepath.Join("~", "configs", "provider.yaml"),
			want:       filepath.Join(home, "configs", "provider.yaml"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveLiveProviderConfigPath(repo, home, tt.configured)
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadLiveConfigUsesTopLevelModel(t *testing.T) {
	configPath := writeLiveProviderTestConfig(t)
	isolateLiveConfigTest(t)

	got, err := loadLiveConfig(repoRoot(t), configPath, t.TempDir(), "")
	if err != nil {
		t.Fatalf("load live config: %v", err)
	}
	if got.name != "alpha:model-a" {
		t.Fatalf("name = %q, want alpha:model-a", got.name)
	}
	if got.cfg.ProviderID != "alpha" || got.cfg.Model != "model-a" {
		t.Fatalf("selection = %s:%s, want alpha:model-a", got.cfg.ProviderID, got.cfg.Model)
	}
}

func TestLoadLiveConfigUsesProviderSmokeOnlyOverride(t *testing.T) {
	configPath := writeLiveProviderTestConfig(t)
	isolateLiveConfigTest(t)

	got, err := loadLiveConfig(repoRoot(t), configPath, t.TempDir(), "beta:model-b")
	if err != nil {
		t.Fatalf("load live config: %v", err)
	}
	if got.name != "beta:model-b" {
		t.Fatalf("name = %q, want beta:model-b", got.name)
	}
	if got.cfg.ProviderID != "beta" || got.cfg.Model != "model-b" {
		t.Fatalf("selection = %s:%s, want beta:model-b", got.cfg.ProviderID, got.cfg.Model)
	}
}

func TestLoadLiveConfigsPreservesNonSelectorProviderEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "juex.yaml")
	body := `model: alpha:model-a
providers:
  - id: alpha
    protocol: openai/chat
    models:
      - id: model-a
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	isolateLiveConfigTest(t)
	t.Setenv(liveProviderConfigEnv, configPath)
	t.Setenv("PROVIDER_API_ID", "must-not-replace-alpha")
	t.Setenv("PROVIDER_API_PROTOCOL", "anthropic/messages")
	t.Setenv("PROVIDER_API_MODEL", "must-not-replace-model-a")
	t.Setenv("PROVIDER_API_BASE", "https://env.example.invalid/v1")
	t.Setenv("PROVIDER_API_KEY", "env-key")
	t.Setenv("PROVIDER_THINKING_EFFORT", "high")
	t.Setenv("PROVIDER_CONTEXT_WINDOW", "12345")

	got := loadLiveConfigs(t)
	if len(got) != 1 {
		t.Fatalf("live configs = %d, want 1", len(got))
	}
	cfg := got[0].cfg
	if cfg.ProviderID != "alpha" || cfg.Model != "model-a" {
		t.Fatalf("selection = %s:%s, want alpha:model-a", cfg.ProviderID, cfg.Model)
	}
	if cfg.ProviderProtocol != "openai/chat" {
		t.Fatalf("protocol = %q, want selected provider protocol openai/chat", cfg.ProviderProtocol)
	}
	if cfg.BaseURL != "https://env.example.invalid/v1" || cfg.APIKey != "env-key" {
		t.Fatalf("environment credentials = base:%q key-set:%t", cfg.BaseURL, cfg.APIKey != "")
	}
	if cfg.ThinkingEffort != "high" || cfg.ContextWindow != 12345 {
		t.Fatalf("environment tuning = effort:%q context:%d", cfg.ThinkingEffort, cfg.ContextWindow)
	}
}

func TestLoadLiveConfigsPreservesYAMLProviderEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "juex.yaml")
	body := `model: alpha:model-a
environment:
  variables:
    PROVIDER_API_ID: must-not-replace-alpha
    PROVIDER_API_MODEL: must-not-replace-model-a
    PROVIDER_API_PROTOCOL: anthropic/messages
    PROVIDER_API_BASE: https://yaml.example.invalid/v1
    PROVIDER_API_KEY: yaml-key
    PROVIDER_THINKING_EFFORT: medium
    PROVIDER_CONTEXT_WINDOW: "23456"
    UNRELATED_SECRET: must-not-be-copied
providers:
  - id: alpha
    protocol: openai/chat
    models:
      - id: model-a
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(liveProviderModelEnv, "ambient:missing")
	t.Setenv("PROVIDER_API_KEY", "ambient-key")
	isolateLiveConfigTest(t)
	t.Setenv(liveProviderConfigEnv, configPath)

	got := loadLiveConfigs(t)
	if len(got) != 1 {
		t.Fatalf("live configs = %d, want 1", len(got))
	}
	cfg := got[0].cfg
	if cfg.ProviderID != "alpha" || cfg.Model != "model-a" {
		t.Fatalf("selection = %s:%s, want alpha:model-a", cfg.ProviderID, cfg.Model)
	}
	if cfg.ProviderProtocol != "openai/chat" {
		t.Fatalf("protocol = %q, want selected provider protocol openai/chat", cfg.ProviderProtocol)
	}
	if cfg.BaseURL != "https://yaml.example.invalid/v1" || cfg.APIKey != "yaml-key" {
		t.Fatalf("YAML environment credentials = base:%q key-set:%t", cfg.BaseURL, cfg.APIKey != "")
	}
	if cfg.ThinkingEffort != "medium" || cfg.ContextWindow != 23456 {
		t.Fatalf("YAML environment tuning = effort:%q context:%d", cfg.ThinkingEffort, cfg.ContextWindow)
	}
	if _, ok := cfg.EnvironmentSnapshot().Lookup("UNRELATED_SECRET"); ok {
		t.Fatal("selected config retained unrelated YAML environment variable")
	}
}

func TestLoadLiveConfigRequiresCompleteProviderModelOverride(t *testing.T) {
	configPath := writeLiveProviderTestConfig(t)
	isolateLiveConfigTest(t)

	_, err := loadLiveConfig(repoRoot(t), configPath, t.TempDir(), "beta")
	if err == nil {
		t.Fatal("load live config succeeded, want provider:model override error")
	}
	if !strings.Contains(err.Error(), "must be a complete provider:model") {
		t.Fatalf("error = %q, want complete provider:model detail", err)
	}
}

func TestLoadLiveConfigRejectsUnusableExistingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "juex.yaml")
	if err := os.WriteFile(configPath, []byte("model: missing:model\nproviders: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	isolateLiveConfigTest(t)

	_, err := loadLiveConfig(repoRoot(t), configPath, t.TempDir(), "")
	if err == nil {
		t.Fatal("load live config succeeded, want unusable provider error")
	}
	if !strings.Contains(err.Error(), "provider not found: missing") {
		t.Fatalf("error = %q, want missing provider detail", err)
	}
}

func TestLoadLiveConfigRejectsMissingExplicitConfig(t *testing.T) {
	isolateLiveConfigTest(t)
	path := filepath.Join(t.TempDir(), "missing.yaml")

	_, err := loadLiveConfig(repoRoot(t), path, t.TempDir(), "")
	if err == nil {
		t.Fatal("load live config succeeded, want missing config error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %q, want missing path %q", err, path)
	}
}

func TestLoadLiveConfigRejectsEmptyCredentials(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "juex.yaml")
	body := `model: alpha:model-a
providers:
  - id: alpha
    protocol: openai/chat
    base_url: https://alpha.example.invalid/v1
    models:
      - id: model-a
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	isolateLiveConfigTest(t)

	_, err := loadLiveConfig(repoRoot(t), configPath, t.TempDir(), "")
	if err == nil {
		t.Fatal("load live config succeeded, want missing credentials error")
	}
	if !strings.Contains(err.Error(), "has no usable credentials") {
		t.Fatalf("error = %q, want missing credentials detail", err)
	}
}

func writeLiveProviderTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "juex.yaml")
	body := `model: alpha:model-a
providers:
  - id: alpha
    protocol: openai/chat
    base_url: https://alpha.example.invalid/v1
    api_key: test-alpha
    models:
      - id: model-a
  - id: beta
    protocol: anthropic/messages
    base_url: https://beta.example.invalid
    api_key: test-beta
    models:
      - id: model-b
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func isolateLiveConfigTest(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	juexHome := filepath.Join(home, ".juex")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", juexHome)
	t.Setenv("CODEX_HOME", filepath.Join(home, "missing-codex-home"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{
		liveProviderConfigEnv,
		liveProviderModelEnv,
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
		"PROVIDER_THINKING_EFFORT",
		"PROVIDER_CONTEXT_WINDOW",
	} {
		unsetLiveConfigTestEnv(t, key)
	}
}

func unsetLiveConfigTestEnv(t *testing.T, key string) {
	t.Helper()
	value, exists := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if exists {
			if err := os.Setenv(key, value); err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
			return
		}
		if err := os.Unsetenv(key); err != nil {
			t.Errorf("clear restored %s: %v", key, err)
		}
	})
}
