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
	t.Setenv("JUEX_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range liveConfigEnvKeys {
		t.Setenv(key, "")
	}
}
