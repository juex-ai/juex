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
		HomeAgentsDir: filepath.Join("/u", ".agents"),
		WorkDir:       filepath.Join("/proj"),
	}
	wantUserSkills := filepath.Join("/u", ".agents", "skills")
	wantProjSkills := filepath.Join("/proj", ".agents", "skills")
	skills := cfg.SkillDirs()
	if len(skills) != 2 || skills[0] != wantUserSkills || skills[1] != wantProjSkills {
		t.Fatalf("skills = %v", skills)
	}
	if want := filepath.Join("/proj", ".agents", "memory"); cfg.MemoryDir() != want {
		t.Fatalf("memory dir = %q, want %q", cfg.MemoryDir(), want)
	}
	if want := filepath.Join("/proj", ".agents", "sessions"); cfg.SessionsDir() != want {
		t.Fatalf("sessions dir = %q, want %q", cfg.SessionsDir(), want)
	}
	mcp := cfg.MCPConfigPaths()
	wantUserMCP := filepath.Join("/u", ".agents", "mcp.json")
	wantProjMCP := filepath.Join("/proj", ".agents", "mcp.json")
	if len(mcp) != 2 || mcp[0] != wantUserMCP || mcp[1] != wantProjMCP {
		t.Fatalf("mcp = %v", mcp)
	}
	dirs := cfg.AgentsMDDirs()
	wantProjAgents := filepath.Join("/proj", ".agents")
	if len(dirs) != 2 || dirs[0] != filepath.Clean("/proj") || dirs[1] != wantProjAgents {
		t.Fatalf("agents md dirs = %v", dirs)
	}
	if cfg.ProjectAgentsDir() != wantProjAgents {
		t.Fatalf("project agents dir = %q, want %q", cfg.ProjectAgentsDir(), wantProjAgents)
	}
}

func TestPaths_EmptyWorkDirReturnsEmpty(t *testing.T) {
	cfg := Config{HomeAgentsDir: filepath.Join("/u", ".agents")}
	if cfg.MemoryDir() != "" || cfg.SessionsDir() != "" || cfg.ProjectAgentsDir() != "" {
		t.Fatalf("empty WorkDir should yield empty work-local paths: %+v", cfg)
	}
	if len(cfg.AgentsMDDirs()) != 0 {
		t.Fatalf("expected empty AgentsMDDirs, got %v", cfg.AgentsMDDirs())
	}
	wantSkills := filepath.Join("/u", ".agents", "skills")
	skills := cfg.SkillDirs()
	if len(skills) != 1 || skills[0] != wantSkills {
		t.Fatalf("skills = %v", skills)
	}
	wantMCP := filepath.Join("/u", ".agents", "mcp.json")
	mcp := cfg.MCPConfigPaths()
	if len(mcp) != 1 || mcp[0] != wantMCP {
		t.Fatalf("mcp = %v", mcp)
	}
}

func TestNewProvider_RequiresType(t *testing.T) {
	cfg := Config{APIKey: "x", Model: "m"}
	if _, err := cfg.NewProvider(); err == nil {
		t.Fatal("expected error for empty type")
	}
}
