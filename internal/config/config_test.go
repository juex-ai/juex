package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.BaseURL != "https://example.com" || cfg.APIKey != "sk-x" || cfg.Model != "gpt-4" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_RejectsLegacyProviderConfig(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := "provider:\n  id: openai\n  api_key: sk-x\n  model: gpt-test\n"
	writeTextFile(t, configPath, body)

	_, err := LoadFromFile(configPath)
	if err == nil || !strings.Contains(err.Error(), "field provider not found") {
		t.Fatalf("err = %v, want legacy provider field rejection", err)
	}
}

func TestLoadFromFile_OSEnvOverridesExplicitConfig(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://file.example", "sk-file", "gpt-file")

	t.Setenv("PROVIDER_API_ID", "anthropic")
	t.Setenv("PROVIDER_API_BASE", "https://env.example")
	t.Setenv("PROVIDER_API_KEY", "sk-env")
	t.Setenv("PROVIDER_API_MODEL", "claude-env")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "anthropic" || cfg.BaseURL != "https://env.example" || cfg.APIKey != "sk-env" || cfg.Model != "claude-env" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_EnvYAMLExtensionUsesYAMLParser(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".env.yaml")
	writeJuexConfig(t, configPath, "openai", "https://yaml.example", "sk-yaml", "gpt-yaml")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.Model != "gpt-yaml" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_UnknownYAMLFieldErrors(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai/gpt-test
providers:
  - id: openai
    unknown_field: true
    api_key: sk-x
    models:
      - id: gpt-test
`
	writeTextFile(t, configPath, body)

	if _, err := LoadFromFile(configPath); err == nil {
		t.Fatal("expected unknown YAML field error")
	}
}

func TestLoad_GlobalRuntimeConfigFallback(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	t.Chdir(work)
	writeJuexConfig(t, filepath.Join(home, ".juex", "juex.yaml"), "openai", "https://global.example", "sk-global", "gpt-global")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != work {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, work)
	}
	if cfg.ProviderID != "openai" || cfg.BaseURL != "https://global.example" || cfg.APIKey != "sk-global" || cfg.Model != "gpt-global" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_WorkConfigFallsBackToGlobalProviderFields(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	t.Chdir(work)
	writeJuexConfig(t, filepath.Join(home, ".juex", "juex.yaml"), "openai", "https://global.example", "sk-global", "gpt-global")
	body := `model: openai/gpt-local
providers:
  - id: openai
    models:
      - id: gpt-local
        thinking_effort: low
        context_window: 128000
`
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), body)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.BaseURL != "https://global.example" || cfg.APIKey != "sk-global" || cfg.Model != "gpt-local" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.ThinkingEffort != "low" || cfg.ContextWindow != 128000 {
		t.Fatalf("model config = thinking:%q context:%d", cfg.ThinkingEffort, cfg.ContextWindow)
	}
}

func TestLoad_DefaultRuntimeConfigPath(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	t.Chdir(dir)
	writeJuexConfig(t, filepath.Join(dir, ".juex", "juex.yaml"), "openai", "https://default.example", "sk-default", "gpt-default")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.BaseURL != "https://default.example" || cfg.APIKey != "sk-default" || cfg.Model != "gpt-default" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_RuntimeConfigPathWhenWorkDirIsDotJuex(t *testing.T) {
	prepareConfigTest(t)
	project := t.TempDir()
	work := filepath.Join(project, ".juex")
	writeJuexConfig(t, filepath.Join(work, "juex.yaml"), "openai", "https://dotjuex.example", "sk-dot", "gpt-dot")
	t.Chdir(work)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != work {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, work)
	}
	if got, want := cfg.RuntimeConfigPath(), filepath.Join(work, "juex.yaml"); got != want {
		t.Fatalf("RuntimeConfigPath = %q, want %q", got, want)
	}
	if cfg.Model != "gpt-dot" || cfg.BaseURL != "https://dotjuex.example" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_DoesNotReadProjectDotEnvByDefault(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	t.Chdir(dir)
	writeTextFile(t, filepath.Join(dir, ".env"), "PROVIDER_API_ID=anthropic\nPROVIDER_API_MODEL=claude\n")
	writeJuexConfig(t, filepath.Join(dir, ".juex", "juex.yaml"), "openai", "https://yaml.example", "sk-yaml", "gpt-yaml")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.Model != "gpt-yaml" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_OSEnvOverridesFile(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	t.Chdir(dir)
	writeJuexConfig(t, filepath.Join(dir, ".juex", "juex.yaml"), "openai", "https://yaml.example", "sk-yaml", "gpt-yaml")

	t.Setenv("PROVIDER_API_ID", "anthropic")
	t.Setenv("PROVIDER_API_BASE", "https://api.anthropic.com")
	t.Setenv("PROVIDER_API_KEY", "k")
	t.Setenv("PROVIDER_API_MODEL", "claude")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "anthropic" || cfg.Model != "claude" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_DefaultsWorkDirToCwd(t *testing.T) {
	prepareConfigTest(t)
	t.Setenv("PROVIDER_API_ID", "openai")
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
		HomeJuexDir:   filepath.Join("/u", ".juex"),
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
	if want := filepath.Join("/proj", ".juex", "juex.yaml"); cfg.RuntimeConfigPath() != want {
		t.Fatalf("runtime config = %q, want %q", cfg.RuntimeConfigPath(), want)
	}
	if want := filepath.Join("/u", ".juex", "juex.yaml"); cfg.HomeRuntimeConfigPath() != want {
		t.Fatalf("home runtime config = %q, want %q", cfg.HomeRuntimeConfigPath(), want)
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
	cfg := Config{HomeAgentsDir: filepath.Join("/u", ".agents"), HomeJuexDir: filepath.Join("/u", ".juex")}
	if cfg.MemoryDir() != "" || cfg.SessionsDir() != "" || cfg.HistoryPath() != "" || cfg.RuntimeConfigPath() != "" || cfg.ProjectAgentsDir() != "" {
		t.Fatalf("empty WorkDir should yield empty work-local paths: %+v", cfg)
	}
	if cfg.HomeRuntimeConfigPath() != filepath.Join("/u", ".juex", "juex.yaml") {
		t.Fatalf("home runtime config = %q", cfg.HomeRuntimeConfigPath())
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

func TestNewProvider_RequiresProviderSelector(t *testing.T) {
	cfg := Config{APIKey: "x", Model: "m"}
	if _, err := cfg.NewProvider(); err == nil {
		t.Fatal("expected error for empty provider selector")
	}
}

func TestLoadFromFile_ThinkingEffort(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai/gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
        thinking_effort: low
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ThinkingEffort != "low" {
		t.Fatalf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, "low")
	}
}

func TestLoadFromFile_ContextWindow(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai/gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
        context_window: 128000
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextWindow != 128000 {
		t.Fatalf("ContextWindow = %d, want 128000", cfg.ContextWindow)
	}
}

func TestLoadFromFile_CompactionConfig(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai/gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
compaction:
  enabled: false
  reserve_tokens: 1000
  keep_recent_tokens: 2000
  tail_turns: 3
  summary_max_tokens: 777
  tool_result_max_chars: 888
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 1000 || cfg.Compaction.KeepRecentTokens != 2000 || cfg.Compaction.TailTurns != 3 || cfg.Compaction.SummaryMaxTokens != 777 || cfg.Compaction.ToolResultMaxChars != 888 {
		t.Fatalf("Compaction = %+v", cfg.Compaction)
	}
}

func TestLoadFromFile_CompactionDefaults(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 16384 || cfg.Compaction.KeepRecentTokens != 20000 || cfg.Compaction.TailTurns != 2 || cfg.Compaction.SummaryMaxTokens != 2048 || cfg.Compaction.ToolResultMaxChars != 2000 {
		t.Fatalf("Compaction defaults = %+v", cfg.Compaction)
	}
}

func TestLoadFromFile_ContextWindowDefaultAndEnvOverride(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")
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
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ThinkingEffort != "" {
		t.Fatalf("ThinkingEffort = %q, want empty", cfg.ThinkingEffort)
	}
}

func TestLoadFromFile_ProviderProfile(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: deepseek/deepseek-chat
providers:
  - id: deepseek
    protocol: openai/chat
    base_url: https://api.deepseek.com
    api_key: sk-x
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
    models:
      - id: deepseek-chat
        context_window: 64000
        headers:
          X-Model: deepseek-chat
        capabilities:
          max_output_tokens: false
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "deepseek" || cfg.ProviderProtocol != "openai/chat" {
		t.Fatalf("provider identity = id:%q protocol:%q", cfg.ProviderID, cfg.ProviderProtocol)
	}
	if cfg.ProviderHeaders["X-Provider"] != "juex" || cfg.ProviderHeaders["X-Model"] != "deepseek-chat" || cfg.ProviderQuery["beta"] != "1" {
		t.Fatalf("headers/query = %+v / %+v", cfg.ProviderHeaders, cfg.ProviderQuery)
	}
	if cfg.ContextWindow != 64000 {
		t.Fatalf("ContextWindow = %d", cfg.ContextWindow)
	}
	if cfg.ProviderCapabilities.Tools == nil || *cfg.ProviderCapabilities.Tools {
		t.Fatalf("tools override = %+v, want false", cfg.ProviderCapabilities.Tools)
	}
	if cfg.ProviderCapabilities.MaxOutputTokens == nil || *cfg.ProviderCapabilities.MaxOutputTokens {
		t.Fatalf("max_output_tokens override = %+v, want false", cfg.ProviderCapabilities.MaxOutputTokens)
	}
	if got := cfg.ProviderCompat.ReasoningReplayFields; len(got) != 1 || got[0] != "reasoning_content" {
		t.Fatalf("compat = %+v", cfg.ProviderCompat)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "deepseek" || profile.Protocol != "openai/chat" || profile.Capabilities.Tools || profile.Capabilities.MaxOutputTokens {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestLoadFromFile_OpenAICodexIDUsesDefaultCodexAuth(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	writeTextFile(t, filepath.Join(codexHome, "auth.json"), `{"auth_mode":"apiKey","OPENAI_API_KEY":"sk-codex"}`)
	configPath := filepath.Join(dir, "juex.yaml")
	writeOpenAICodexConfig(t, configPath, "")
	t.Setenv("CODEX_HOME", codexHome)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai-codex" || cfg.APIKey != "sk-codex" {
		t.Fatalf("cfg = %+v", cfg)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Protocol != "openai-codex/responses" {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestLoadFromFile_ProviderProfileEnvOverrides(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://file.example", "sk-file", "gpt-file")
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
	if profile.Protocol != "openai/responses" {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestLoadFromFile_CodexAuthUsesDefaultCachedAPIKey(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	writeTextFile(t, filepath.Join(codexHome, "auth.json"), `{"auth_mode":"apiKey","OPENAI_API_KEY":"sk-codex"}`)
	configPath := filepath.Join(dir, "juex.yaml")
	writeOpenAICodexConfig(t, configPath, "")
	t.Setenv("CODEX_HOME", codexHome)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-codex" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_CodexAuthUsesChatGPTTokenHeaders(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	idToken := fakeCodexIDToken(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         "acct-from-jwt",
			"chatgpt_account_is_fedramp": true,
		},
	})
	authJSON := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": "chatgpt-access",
			"id_token":     idToken,
		},
	}
	authBytes, err := json.Marshal(authJSON)
	if err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, filepath.Join(codexHome, "auth.json"), string(authBytes))
	configPath := filepath.Join(dir, "juex.yaml")
	writeOpenAICodexConfig(t, configPath, "")
	t.Setenv("CODEX_HOME", codexHome)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "chatgpt-access" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
	if cfg.ProviderID != "openai-codex" || cfg.ProviderProtocol != "openai-codex/responses" {
		t.Fatalf("provider route = id:%q protocol:%q", cfg.ProviderID, cfg.ProviderProtocol)
	}
	if cfg.ProviderHeaders["ChatGPT-Account-ID"] != "acct-from-jwt" || cfg.ProviderHeaders["X-OpenAI-Fedramp"] != "true" {
		t.Fatalf("headers = %+v", cfg.ProviderHeaders)
	}
}

func TestLoadFromFile_CodexAuthExplicitAPIKeyWins(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeOpenAICodexConfig(t, configPath, "sk-explicit")
	t.Setenv("CODEX_HOME", filepath.Join(dir, "missing-codex-home"))

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-explicit" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
}

func TestLoadFromFile_CodexAuthRuntimeConfigCanBeOverridden(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	writeOpenAICodexConfig(t, filepath.Join(work, ".juex", "juex.yaml"), "")
	overrideConfig := filepath.Join(work, "override.yaml")
	writeJuexConfig(t, overrideConfig, "openai", "https://example.com", "sk-override", "gpt-test")

	cfg, err := LoadFromFileForWorkDir(overrideConfig, work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-override" || cfg.Model != "gpt-test" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_CustomProtocolOverridesRuntimePresetIdentity(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	writeOpenAICodexConfig(t, filepath.Join(work, ".juex", "juex.yaml"), "")
	overrideConfig := filepath.Join(work, "override.yaml")
	body := `model: local-proxy/custom-model
providers:
  - id: local-proxy
    protocol: openai/chat
    base_url: https://example.com
    api_key: sk-override
    models:
      - id: custom-model
`
	writeTextFile(t, overrideConfig, body)

	cfg, err := LoadFromFileForWorkDir(overrideConfig, work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "local-proxy" || cfg.ProviderProtocol != "openai/chat" {
		t.Fatalf("cfg identity = id:%q protocol:%q", cfg.ProviderID, cfg.ProviderProtocol)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "local-proxy" || profile.Protocol != "openai/chat" {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestLoadFromFile_CodexAuthMissingCredentialErrors(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	writeTextFile(t, filepath.Join(codexHome, "auth.json"), `{"auth_mode":"chatgpt","tokens":{}}`)
	configPath := filepath.Join(dir, "juex.yaml")
	writeOpenAICodexConfig(t, configPath, "")
	t.Setenv("CODEX_HOME", codexHome)

	if _, err := LoadFromFile(configPath); err == nil {
		t.Fatal("expected missing codex credential error")
	}
}

func prepareConfigTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, "missing-codex-home"))
	return home
}

func writeJuexConfig(t *testing.T, path, id, base, key, model string) {
	t.Helper()
	body := "model: " + id + "/" + model + "\n" +
		"providers:\n" +
		"  - id: " + id + "\n" +
		"    base_url: " + base + "\n" +
		"    api_key: " + key + "\n" +
		"    models:\n" +
		"      - id: " + model + "\n"
	writeTextFile(t, path, body)
}

func writeOpenAICodexConfig(t *testing.T, path, apiKey string) {
	t.Helper()
	body := "model: openai-codex/gpt-test\n" +
		"providers:\n" +
		"  - id: openai-codex\n"
	if apiKey != "" {
		body += "    api_key: " + apiKey + "\n"
	}
	body += "    models:\n" +
		"      - id: gpt-test\n"
	writeTextFile(t, path, body)
}

func writeTextFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeCodexIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}
