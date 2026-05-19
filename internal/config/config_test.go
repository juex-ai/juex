package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := "provider:\n  type: openai\n  base_url: https://example.com\n  api_key: sk-x\n  model: gpt-4\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROVIDER_API_TYPE", "")
	t.Setenv("PROVIDER_API_BASE", "")
	t.Setenv("PROVIDER_API_KEY", "")
	t.Setenv("PROVIDER_API_MODEL", "")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "openai" || cfg.BaseURL != "https://example.com" || cfg.APIKey != "sk-x" || cfg.Model != "gpt-4" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_OSEnvOverridesExplicitConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://file.example", "sk-file", "gpt-file")

	t.Setenv("PROVIDER_API_TYPE", "anthropic")
	t.Setenv("PROVIDER_API_BASE", "https://env.example")
	t.Setenv("PROVIDER_API_KEY", "sk-env")
	t.Setenv("PROVIDER_API_MODEL", "claude-env")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "anthropic" || cfg.BaseURL != "https://env.example" || cfg.APIKey != "sk-env" || cfg.Model != "claude-env" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_EnvYAMLExtensionUsesYAMLParser(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".env.yaml")
	writeJuexConfig(t, configPath, "openai", "https://yaml.example", "sk-yaml", "gpt-yaml")
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "openai" || cfg.Model != "gpt-yaml" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_DefaultRuntimeConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}
	writeJuexConfig(t, filepath.Join(dir, ".juex", "juex.yaml"), "openai", "https://default.example", "sk-default", "gpt-default")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "openai" || cfg.BaseURL != "https://default.example" || cfg.APIKey != "sk-default" || cfg.Model != "gpt-default" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_DoesNotReadProjectDotEnvByDefault(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PROVIDER_API_TYPE=anthropic\nPROVIDER_API_MODEL=claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJuexConfig(t, filepath.Join(dir, ".juex", "juex.yaml"), "openai", "https://yaml.example", "sk-yaml", "gpt-yaml")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderType != "openai" || cfg.Model != "gpt-yaml" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_OSEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeJuexConfig(t, filepath.Join(dir, ".juex", "juex.yaml"), "openai", "https://yaml.example", "sk-yaml", "gpt-yaml")

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
	if want := filepath.Join("/proj", ".juex", "memory"); cfg.MemoryDir() != want {
		t.Fatalf("memory dir = %q, want %q", cfg.MemoryDir(), want)
	}
	if want := filepath.Join("/proj", ".juex", "sessions"); cfg.SessionsDir() != want {
		t.Fatalf("sessions dir = %q, want %q", cfg.SessionsDir(), want)
	}
	if want := filepath.Join("/proj", ".juex", "history.json"); cfg.HistoryPath() != want {
		t.Fatalf("history path = %q, want %q", cfg.HistoryPath(), want)
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
	if cfg.MemoryDir() != "" || cfg.SessionsDir() != "" || cfg.HistoryPath() != "" || cfg.RuntimeConfigPath() != "" || cfg.ProjectAgentsDir() != "" {
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

func writeJuexConfig(t *testing.T, path, typ, base, key, model string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "provider:\n  type: " + typ + "\n  base_url: " + base + "\n  api_key: " + key + "\n  model: " + model + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromFile_ThinkingEffort(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := "provider:\n  type: openai\n  base_url: https://example.com\n  api_key: sk-x\n  model: gpt-4\n  thinking_effort: low\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ThinkingEffort != "low" {
		t.Fatalf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, "low")
	}
}

func TestLoadFromFile_ContextWindow(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := "provider:\n  type: openai\n  base_url: https://example.com\n  api_key: sk-x\n  model: gpt-4\n  context_window: 128000\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextWindow != 128000 {
		t.Fatalf("ContextWindow = %d, want 128000", cfg.ContextWindow)
	}
}

func TestLoadFromFile_CompactionConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := "provider:\n  type: openai\n  base_url: https://example.com\n  api_key: sk-x\n  model: gpt-4\ncompaction:\n  enabled: false\n  reserve_tokens: 1000\n  keep_recent_tokens: 2000\n  tail_turns: 3\n  summary_max_tokens: 777\n  tool_result_max_chars: 888\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 1000 || cfg.Compaction.KeepRecentTokens != 2000 || cfg.Compaction.TailTurns != 3 || cfg.Compaction.SummaryMaxTokens != 777 || cfg.Compaction.ToolResultMaxChars != 888 {
		t.Fatalf("Compaction = %+v", cfg.Compaction)
	}
}

func TestLoadFromFile_CompactionDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 16384 || cfg.Compaction.KeepRecentTokens != 20000 || cfg.Compaction.TailTurns != 2 || cfg.Compaction.SummaryMaxTokens != 2048 || cfg.Compaction.ToolResultMaxChars != 2000 {
		t.Fatalf("Compaction defaults = %+v", cfg.Compaction)
	}
}

func TestLoadFromFile_ContextWindowDefaultAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("PROVIDER_CONTEXT_WINDOW", "64000")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextWindow != 64000 {
		t.Fatalf("ContextWindow = %d, want env override 64000", cfg.ContextWindow)
	}

	t.Setenv("PROVIDER_CONTEXT_WINDOW", "")
	cfg, err = LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextWindow != DefaultContextWindow {
		t.Fatalf("ContextWindow = %d, want default %d", cfg.ContextWindow, DefaultContextWindow)
	}
}

func TestLoadFromFile_ThinkingEffortEmpty(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := "provider:\n  type: openai\n  base_url: https://example.com\n  api_key: sk-x\n  model: gpt-4\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ThinkingEffort != "" {
		t.Fatalf("ThinkingEffort = %q, want empty", cfg.ThinkingEffort)
	}
}

func TestLoadFromFile_ProviderProfile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `provider:
  id: deepseek
  protocol: openai-compatible/chat
  base_url: https://api.deepseek.com
  api_key: sk-x
  model: deepseek-chat
  headers:
    X-Provider: juex
  query:
    beta: "1"
  capabilities:
    tools: false
    reasoning_replay: true
  compat:
    reasoning_replay_fields:
      - reasoning_content
`
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "deepseek" || cfg.ProviderProtocol != "openai-compatible/chat" {
		t.Fatalf("provider identity = id:%q protocol:%q", cfg.ProviderID, cfg.ProviderProtocol)
	}
	if cfg.ProviderHeaders["X-Provider"] != "juex" || cfg.ProviderQuery["beta"] != "1" {
		t.Fatalf("headers/query = %+v / %+v", cfg.ProviderHeaders, cfg.ProviderQuery)
	}
	if cfg.ProviderCapabilities.Tools == nil || *cfg.ProviderCapabilities.Tools {
		t.Fatalf("tools override = %+v, want false", cfg.ProviderCapabilities.Tools)
	}
	if got := cfg.ProviderCompat.ReasoningReplayFields; len(got) != 1 || got[0] != "reasoning_content" {
		t.Fatalf("compat = %+v", cfg.ProviderCompat)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Type != "openai" || profile.Protocol != "openai-compatible/chat" || profile.Capabilities.Tools {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestLoadFromFile_ProviderProfileEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://file.example", "sk-file", "gpt-file")
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("PROVIDER_API_ID", "openai")
	t.Setenv("PROVIDER_API_PROTOCOL", "openai/responses")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.ProviderProtocol != "openai/responses" {
		t.Fatalf("cfg = %+v", cfg)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Type != "openai" || profile.Protocol != "openai/responses" {
		t.Fatalf("profile = %+v", profile)
	}
}
