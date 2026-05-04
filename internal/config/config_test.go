package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	body := "PROVIDER_API_TYPE=openai\nPROVIDER_API_BASE=\"https://example.com\"\nPROVIDER_API_KEY=sk-x\nPROVIDER_API_MODEL=gpt-4\n# comment\n"
	os.WriteFile(envPath, []byte(body), 0o644)

	t.Setenv("PROVIDER_API_TYPE", "")
	t.Setenv("PROVIDER_API_BASE", "")
	t.Setenv("PROVIDER_API_KEY", "")
	t.Setenv("PROVIDER_API_MODEL", "")

	cfg, err := LoadFromFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "openai" || cfg.BaseURL != "https://example.com" || cfg.APIKey != "sk-x" || cfg.Model != "gpt-4" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_OSEnvOverridesFile(t *testing.T) {
	t.Setenv("PROVIDER_API_TYPE", "anthropic")
	t.Setenv("PROVIDER_API_BASE", "https://api.anthropic.com")
	t.Setenv("PROVIDER_API_KEY", "k")
	t.Setenv("PROVIDER_API_MODEL", "claude")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "anthropic" || cfg.Model != "claude" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_DefaultsWorkDirToCwd(t *testing.T) {
	t.Setenv("PROVIDER_API_TYPE", "openai")
	t.Setenv("PROVIDER_API_BASE", "https://x")
	t.Setenv("PROVIDER_API_KEY", "k")
	t.Setenv("PROVIDER_API_MODEL", "m")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wantWD, _ := os.Getwd()
	if cfg.WorkDir != wantWD {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, wantWD)
	}
}

func TestSkillDirs_AndPaths(t *testing.T) {
	cfg := Config{
		HomeAgentsDir: "/u/.agents",
		WorkDir:       "/proj",
	}
	skills := cfg.SkillDirs()
	if len(skills) != 2 || skills[0] != "/u/.agents/skills" || skills[1] != "/proj/.agents/skills" {
		t.Fatalf("skills = %v", skills)
	}
	if cfg.MemoryDir() != "/proj/.agents/memory" {
		t.Fatalf("memory dir = %q", cfg.MemoryDir())
	}
	if cfg.SessionsDir() != "/proj/.agents/sessions" {
		t.Fatalf("sessions dir = %q", cfg.SessionsDir())
	}
	mcp := cfg.MCPConfigPaths()
	if len(mcp) != 2 || mcp[0] != "/u/.agents/mcp.json" || mcp[1] != "/proj/.agents/mcp.json" {
		t.Fatalf("mcp = %v", mcp)
	}
	dirs := cfg.AgentsMDDirs()
	if len(dirs) != 2 || dirs[0] != "/proj" || dirs[1] != "/proj/.agents" {
		t.Fatalf("agents md dirs = %v", dirs)
	}
	if cfg.ProjectAgentsDir() != "/proj/.agents" {
		t.Fatalf("project agents dir = %q", cfg.ProjectAgentsDir())
	}
}

func TestPaths_EmptyWorkDirReturnsEmpty(t *testing.T) {
	cfg := Config{HomeAgentsDir: "/u/.agents"}
	if cfg.MemoryDir() != "" || cfg.SessionsDir() != "" || cfg.ProjectAgentsDir() != "" {
		t.Fatalf("empty WorkDir should yield empty work-local paths: %+v", cfg)
	}
	if len(cfg.AgentsMDDirs()) != 0 {
		t.Fatalf("expected empty AgentsMDDirs, got %v", cfg.AgentsMDDirs())
	}
	skills := cfg.SkillDirs()
	if len(skills) != 1 || skills[0] != "/u/.agents/skills" {
		t.Fatalf("skills = %v", skills)
	}
	mcp := cfg.MCPConfigPaths()
	if len(mcp) != 1 || mcp[0] != "/u/.agents/mcp.json" {
		t.Fatalf("mcp = %v", mcp)
	}
}

func TestNewProvider_RequiresType(t *testing.T) {
	cfg := Config{APIKey: "x", Model: "m"}
	if _, err := cfg.NewProvider(); err == nil {
		t.Fatal("expected error for empty type")
	}
}
