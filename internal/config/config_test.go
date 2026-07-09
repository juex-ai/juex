package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestConfigObservablesPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{WorkDir: dir}
	if got, want := cfg.ObservablesConfigPath(), filepath.Join(dir, ".juex", "observables.json"); got != want {
		t.Fatalf("ObservablesConfigPath() = %q, want %q", got, want)
	}
	if got, want := cfg.ObservablesStateDir(), filepath.Join(dir, ".juex", "observables"); got != want {
		t.Fatalf("ObservablesStateDir() = %q, want %q", got, want)
	}
}

func TestLoadFromFile_ModelIDCanContainSlash(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: local-proxy:meta-llama/Llama-3-8b-chat
providers:
  - id: local-proxy
    protocol: openai/chat
    base_url: https://local.example
    api_key: sk-local
    models:
      - id: meta-llama/Llama-3-8b-chat
        context_window: 32000
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "local-proxy" || cfg.Model != "meta-llama/Llama-3-8b-chat" || cfg.ContextWindow != 32000 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseModelRef(t *testing.T) {
	ref, err := ParseModelRef(" local-proxy:meta-llama/Llama-3-8b-chat ")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ProviderID != "local-proxy" || ref.ModelID != "meta-llama/Llama-3-8b-chat" {
		t.Fatalf("ref = %+v", ref)
	}
	if got := ref.String(); got != "local-proxy:meta-llama/Llama-3-8b-chat" {
		t.Fatalf("String() = %q", got)
	}

	for _, raw := range []string{"", "provider-only", "/model", "provider/", "provider/model", ":model", "provider:"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseModelRef(raw); err == nil || !strings.Contains(err.Error(), "provider:model") {
				t.Fatalf("ParseModelRef(%q) err = %v, want provider:model error", raw, err)
			}
		})
	}
}

func TestLoadFromFileRejectsProviderIDWithModelSeparator(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: bad:provider:gpt
providers:
  - id: bad:provider
    base_url: https://bad.example
    api_key: sk-bad
    models:
      - id: gpt
`
	writeTextFile(t, configPath, body)

	_, err := LoadFromFile(configPath)
	if err == nil || !strings.Contains(err.Error(), `provider "bad:provider" id must not contain ':'`) {
		t.Fatalf("err = %v, want provider id separator error", err)
	}
}

func TestConfigApplyModelOverride(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-default
providers:
  - id: openai
    base_url: https://openai.example
    api_key: sk-openai
    models:
      - id: gpt-default
  - id: local-proxy
    protocol: openai/chat
    base_url: https://local.example
    api_key: sk-local
    models:
      - id: meta-llama/Llama-3-8b-chat
        context_window: 32000
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyModelOverride(" local-proxy:meta-llama/Llama-3-8b-chat "); err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "local-proxy" || cfg.ProviderProtocol != "openai/chat" || cfg.BaseURL != "https://local.example" || cfg.APIKey != "sk-local" || cfg.Model != "meta-llama/Llama-3-8b-chat" || cfg.ContextWindow != 32000 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestConfigApplyModelOverrideRejectsUnknownModel(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.ApplyModelOverride("openai:missing")
	if err == nil || !strings.Contains(err.Error(), `model "openai:missing" references unknown model "missing" for provider "openai"`) {
		t.Fatalf("err = %v, want unknown model error", err)
	}
}

func TestLoadFromFileWithModelOverrideKeepsNonSelectorEnv(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-default
providers:
  - id: openai
    base_url: https://openai.example
    api_key: sk-openai
    models:
      - id: gpt-default
  - id: anthropic
    base_url: https://anthropic.example
    api_key: sk-anthropic
    models:
      - id: claude-sonnet
`
	writeTextFile(t, configPath, body)
	t.Setenv("PROVIDER_API_ID", "openai")
	t.Setenv("PROVIDER_API_MODEL", "gpt-default")
	t.Setenv("PROVIDER_API_BASE", "https://env.example")
	t.Setenv("PROVIDER_API_KEY", "sk-env")

	cfg, err := LoadFromFileForWorkDirWithModelOverride(configPath, dir, "anthropic:claude-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "anthropic" || cfg.Model != "claude-sonnet" || cfg.BaseURL != "https://env.example" || cfg.APIKey != "sk-env" {
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

func TestLoadFromFile_RejectsScalarShellConfig(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, configPath, "shell: powershell\n")

	_, err := LoadFromFile(configPath)
	if err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("err = %v, want scalar shell config rejection", err)
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
	body := `model: openai:gpt-test
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
	body := `model: openai:gpt-local
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

func TestLoad_LegacyRuntimeBudgetKeysAreIgnored(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	t.Chdir(work)
	global := `model: openai:gpt-global
providers:
  - id: openai
    base_url: https://global.example
    api_key: sk-global
    models:
      - id: gpt-global
runtime:
  max_iters: 5
  max_duration: 20s
`
	local := `runtime:
  max_iters: 0
  max_duration: forever
`
	writeTextFile(t, filepath.Join(home, ".juex", "juex.yaml"), global)
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), local)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.Model != "gpt-global" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoad_SandboxDefaultsAndOverrides(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	t.Chdir(work)
	writeJuexConfig(t, filepath.Join(home, ".juex", "juex.yaml"), "openai", "https://global.example", "sk-global", "gpt-global")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Enabled {
		t.Fatalf("sandbox enabled = true, want default false")
	}
	if cfg.Sandbox.FileSystem.OutsideWorkspace != OutsideWorkspaceReadWrite || !cfg.Sandbox.Network.Enabled {
		t.Fatalf("sandbox defaults = %+v", cfg.Sandbox)
	}
	if len(cfg.Sandbox.FileSystem.BlockedPaths) != 0 {
		t.Fatalf("blocked paths default = %#v, want empty", cfg.Sandbox.FileSystem.BlockedPaths)
	}

	local := `sandbox:
  enabled: true
  file_system:
    outside_workspace: read_only
    blocked_paths:
      - ~/.ssh
  network:
    enabled: false
`
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), local)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sandbox.Enabled || cfg.Sandbox.FileSystem.OutsideWorkspace != OutsideWorkspaceReadOnly || cfg.Sandbox.Network.Enabled {
		t.Fatalf("sandbox override = %+v", cfg.Sandbox)
	}
	if got, want := strings.Join(cfg.Sandbox.FileSystem.BlockedPaths, ","), "~/.ssh"; got != want {
		t.Fatalf("blocked paths = %q, want %q", got, want)
	}
}

func TestLoad_SandboxMergesAcrossConfigLayers(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	t.Chdir(work)
	global := `model: openai:gpt-global
providers:
  - id: openai
    base_url: https://global.example
    api_key: sk-global
    models:
      - id: gpt-global
sandbox:
  enabled: true
  file_system:
    outside_workspace: read_only
    blocked_paths:
      - ~/.ssh
      - .env
`
	local := `sandbox:
  file_system:
    blocked_paths:
      - ~/.aws
      - ~/.ssh
  network:
    enabled: false
`
	writeTextFile(t, filepath.Join(home, ".juex", "juex.yaml"), global)
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), local)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sandbox.Enabled || cfg.Sandbox.FileSystem.OutsideWorkspace != OutsideWorkspaceReadOnly || cfg.Sandbox.Network.Enabled {
		t.Fatalf("sandbox merged policy = %+v", cfg.Sandbox)
	}
	wantBlocked := []string{"~/.ssh", ".env", "~/.aws"}
	if strings.Join(cfg.Sandbox.FileSystem.BlockedPaths, "\x00") != strings.Join(wantBlocked, "\x00") {
		t.Fatalf("blocked paths = %#v, want %#v", cfg.Sandbox.FileSystem.BlockedPaths, wantBlocked)
	}
}

func TestLoad_SandboxRejectsInvalidOutsideWorkspace(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	body := `sandbox:
  file_system:
    outside_workspace: maybe
`
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), body)

	_, err := LoadForWorkDir(work)
	if err == nil || !strings.Contains(err.Error(), "sandbox.file_system.outside_workspace") || !strings.Contains(err.Error(), "read_write, read_only") {
		t.Fatalf("err = %v, want sandbox enum error", err)
	}
}

func TestLoad_SandboxRejectsDeniedOutsideWorkspace(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	body := `sandbox:
  file_system:
    outside_workspace: denied
`
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), body)

	_, err := LoadForWorkDir(work)
	if err == nil || !strings.Contains(err.Error(), "outside_workspace") || !strings.Contains(err.Error(), "read_write, read_only") {
		t.Fatalf("err = %v, want denied to be rejected", err)
	}
}

func TestLoad_SandboxRejectsEmptyBlockedPath(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	body := `sandbox:
  file_system:
    blocked_paths:
      - " "
`
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), body)

	_, err := LoadForWorkDir(work)
	if err == nil || !strings.Contains(err.Error(), "blocked_paths") {
		t.Fatalf("err = %v, want blocked_paths validation error", err)
	}
}

func TestLoad_GlobalHooksDoNotRequireTrust(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	body := `model: openai:gpt-global
providers:
  - id: openai
    base_url: https://global.example
    api_key: sk-global
    models:
      - id: gpt-global
hooks:
  commands:
    - name: global-context
      events: [UserPromptSubmit]
      command: ["echo", "{}"]
`
	writeTextFile(t, filepath.Join(home, ".juex", "juex.yaml"), body)

	cfg, err := LoadForWorkDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.Commands) != 1 || cfg.Hooks.Commands[0].Name != "global-context" || cfg.Hooks.Commands[0].Source != "user" {
		t.Fatalf("hooks = %+v", cfg.Hooks.Commands)
	}
}

func TestLoad_ProjectHooksRequireTrust(t *testing.T) {
	prepareConfigTest(t)
	work := t.TempDir()
	body := `model: openai:gpt-local
providers:
  - id: openai
    base_url: https://local.example
    api_key: sk-local
    models:
      - id: gpt-local
hooks:
  commands:
    - name: project-context
      events: [UserPromptSubmit]
      command: ["echo", "{}"]
`
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), body)

	_, err := LoadForWorkDir(work)
	if err == nil || !strings.Contains(err.Error(), "hooks.trusted: true") {
		t.Fatalf("err = %v, want project trust error", err)
	}
}

func TestLoad_HooksMergeInConfigOrder(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	global := `model: openai:gpt-global
providers:
  - id: openai
    base_url: https://global.example
    api_key: sk-global
    models:
      - id: gpt-global
hooks:
  commands:
    - name: global-context
      events: [UserPromptSubmit]
      command: ["echo", "{}"]
`
	local := `model: openai:gpt-global
hooks:
  trusted: true
  commands:
    - name: project-guard
      events: [PreToolUse]
      command: ["echo", "{}"]
`
	writeTextFile(t, filepath.Join(home, ".juex", "juex.yaml"), global)
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), local)

	cfg, err := LoadForWorkDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.Commands) != 2 {
		t.Fatalf("hooks = %+v", cfg.Hooks.Commands)
	}
	if cfg.Hooks.Commands[0].Name != "global-context" || cfg.Hooks.Commands[0].Source != "user" {
		t.Fatalf("first hook = %+v", cfg.Hooks.Commands[0])
	}
	if cfg.Hooks.Commands[1].Name != "project-guard" || cfg.Hooks.Commands[1].Source != "project" {
		t.Fatalf("second hook = %+v", cfg.Hooks.Commands[1])
	}
}

func TestLoad_WorkShellEmptyResetsGlobalShell(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()
	t.Chdir(work)
	global := `model: openai:gpt-global
providers:
  - id: openai
    base_url: https://global.example
    api_key: sk-global
    models:
      - id: gpt-global
shell:
  profile: custom
  binary: ` + quoteYAMLString(os.Args[0]) + `
  family: posix
  args: ["-test.run=TestNoop"]
  path_style: posix
`
	local := `shell: {}
`
	writeTextFile(t, filepath.Join(home, ".juex", "juex.yaml"), global)
	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), local)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Shell.Profile == "custom" {
		t.Fatalf("work-local shell: {} should reset user-global shell config, got %+v", cfg.Shell)
	}
	if !strings.HasPrefix(cfg.Shell.Source, "auto:") {
		t.Fatalf("shell source = %q, want auto source after reset", cfg.Shell.Source)
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

func TestResolveShellProfile_AutoWindowsPrefersPowerShell(t *testing.T) {
	profile, err := ResolveShellProfile(ShellConfig{}, ShellResolveOptions{
		RuntimeOS:   "windows",
		RuntimeArch: "amd64",
		LookupEnv:   func(string) (string, bool) { return "", false },
		LookPath: func(name string) (string, error) {
			switch name {
			case "pwsh":
				return `C:\Tools\pwsh.exe`, nil
			case "powershell.exe", "cmd.exe":
				return `C:\Windows\System32\` + name, nil
			default:
				return "", os.ErrNotExist
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Profile != "powershell" || profile.Family != "powershell" || profile.Binary != `C:\Tools\pwsh.exe` {
		t.Fatalf("profile = %+v", profile)
	}
	if strings.Join(profile.Args, " ") != "-NoProfile -Command" || profile.PathStyle != "windows" || profile.Source != "auto:windows" {
		t.Fatalf("profile metadata = %+v", profile)
	}
	if profile.RuntimeOS != "windows" || profile.RuntimeArch != "amd64" {
		t.Fatalf("runtime metadata = %+v", profile)
	}
}

func TestResolveShellProfile_LinuxWSLStaysPOSIX(t *testing.T) {
	profile, err := ResolveShellProfile(ShellConfig{}, ShellResolveOptions{
		RuntimeOS:   "linux",
		RuntimeArch: "amd64",
		LookupEnv: func(key string) (string, bool) {
			if key == "WSL_DISTRO_NAME" {
				return "Ubuntu", true
			}
			return "", false
		},
		LookPath: func(name string) (string, error) {
			if name == "bash" {
				return "/usr/bin/bash", nil
			}
			return "", os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Profile != "bash" || profile.Family != "posix" || profile.Environment != "wsl" {
		t.Fatalf("profile = %+v, want WSL environment with POSIX shell", profile)
	}
}

func TestResolveShellProfile_BuiltinRejectsNonBinaryOverrides(t *testing.T) {
	_, err := ResolveShellProfile(ShellConfig{Profile: "powershell", Binary: "bash"}, ShellResolveOptions{
		RuntimeOS:   "windows",
		RuntimeArch: "amd64",
		LookupEnv:   func(string) (string, bool) { return "", false },
		LookPath: func(name string) (string, error) {
			if name == "bash" {
				return `/usr/bin/bash`, nil
			}
			return "", os.ErrNotExist
		},
	})
	if err == nil || !strings.Contains(err.Error(), "shell.profile powershell cannot use binary bash") {
		t.Fatalf("err = %v, want profile/binary mismatch", err)
	}
}

func TestResolveShellProfile_CustomRequiresFields(t *testing.T) {
	_, err := ResolveShellProfile(ShellConfig{Profile: "custom", Binary: os.Args[0], Family: "posix"}, ShellResolveOptions{
		RuntimeOS:   "linux",
		RuntimeArch: "amd64",
		LookupEnv:   func(string) (string, bool) { return "", false },
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
	})
	if err == nil || !strings.Contains(err.Error(), "custom") || !strings.Contains(err.Error(), "args") {
		t.Fatalf("err = %v, want custom missing args error", err)
	}
}

func TestResolveShellProfile_CustomPathWithSeparatorBecomesAbsolute(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	binary := filepath.Join("bin", "custom-shell")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	profile, err := ResolveShellProfile(ShellConfig{
		Profile:   "custom",
		Binary:    binary,
		Family:    "posix",
		Args:      []string{"-c"},
		PathStyle: "posix",
	}, ShellResolveOptions{
		RuntimeOS:   "linux",
		RuntimeArch: "amd64",
		LookupEnv:   func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(profile.Binary) {
		t.Fatalf("binary = %q, want absolute path", profile.Binary)
	}
	if !strings.HasSuffix(filepath.ToSlash(profile.Binary), "/bin/custom-shell") {
		t.Fatalf("binary = %q, want resolved custom shell path", profile.Binary)
	}
}

func TestLoad_EnableUserGlobalResourcesDefaultsAndOverrides(t *testing.T) {
	home := prepareConfigTest(t)
	work := t.TempDir()

	cfg, err := LoadForWorkDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableUserGlobalResources {
		t.Fatal("EnableUserGlobalResources should default to true")
	}

	writeTextFile(t, filepath.Join(home, ".juex", "juex.yaml"), "enable_user_global_resources: 0\n")
	cfg, err = LoadForWorkDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableUserGlobalResources {
		t.Fatal("global enable_user_global_resources: 0 should disable user-global resources")
	}
	if cfg.GlobalAgentsMDPath() != "" {
		t.Fatalf("GlobalAgentsMDPath = %q, want empty", cfg.GlobalAgentsMDPath())
	}
	if cfg.HomeExtensionsDir() != "" {
		t.Fatalf("HomeExtensionsDir = %q, want empty", cfg.HomeExtensionsDir())
	}
	if got := cfg.SkillDirs(); len(got) != 1 || got[0] != filepath.Join(work, ".agents", "skills") {
		t.Fatalf("SkillDirs = %v", got)
	}
	if got := cfg.MCPConfigPaths(); len(got) != 1 || got[0] != filepath.Join(work, ".agents", "mcp.json") {
		t.Fatalf("MCPConfigPaths = %v", got)
	}

	writeTextFile(t, filepath.Join(work, ".juex", "juex.yaml"), "enable_user_global_resources: 1\n")
	cfg, err = LoadForWorkDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableUserGlobalResources {
		t.Fatal("work-local enable_user_global_resources: 1 should override global false")
	}

	override := filepath.Join(work, "override.yaml")
	writeTextFile(t, override, "enable_user_global_resources: false\n")
	cfg, err = LoadFromFileForWorkDir(override, work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableUserGlobalResources {
		t.Fatal("--config override false should win over work-local true")
	}
}

func TestLoadFromFile_EnableUserGlobalResourcesBoolValues(t *testing.T) {
	cases := map[string]bool{
		"true":  true,
		"false": false,
		"1":     true,
		"0":     false,
	}
	for value, want := range cases {
		t.Run(value, func(t *testing.T) {
			prepareConfigTest(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "juex.yaml")
			writeTextFile(t, path, "enable_user_global_resources: "+value+"\n")
			cfg, err := LoadFromFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.EnableUserGlobalResources != want {
				t.Fatalf("EnableUserGlobalResources = %v, want %v", cfg.EnableUserGlobalResources, want)
			}
		})
	}
}

func TestLoadFromFile_EnableUserGlobalResourcesRejectsInvalidBool(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, path, "enable_user_global_resources: maybe\n")

	_, err := LoadFromFile(path)
	if err == nil || !strings.Contains(err.Error(), "expected boolean value") {
		t.Fatalf("err = %v, want boolean parse error", err)
	}
}

func TestLoadForWorkDirNormalizesRelativeWorkDir(t *testing.T) {
	prepareConfigTest(t)
	t.Setenv("PROVIDER_API_ID", "openai")
	t.Setenv("PROVIDER_API_BASE", "https://x")
	t.Setenv("PROVIDER_API_KEY", "k")
	t.Setenv("PROVIDER_API_MODEL", "m")
	base := t.TempDir()
	t.Chdir(base)
	if err := os.MkdirAll("workspace", 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForWorkDir("workspace")
	if err != nil {
		t.Fatal(err)
	}
	wantWD := filepath.Join(base, "workspace")
	if cfg.WorkDir != wantWD {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, wantWD)
	}
}

func TestSkillDirs_AndPaths(t *testing.T) {
	cfg := Config{
		HomeAgentsDir:             filepath.Join("/u", ".agents"),
		HomeJuexDir:               filepath.Join("/u", ".juex"),
		WorkDir:                   filepath.Join("/proj"),
		EnableUserGlobalResources: true,
	}
	wantUserSkills := filepath.Join("/u", ".agents", "skills")
	wantProjSkills := filepath.Join("/proj", ".agents", "skills")
	wantUserExtensions := filepath.Join("/u", ".juex", "extensions")
	wantProjExtensions := filepath.Join("/proj", ".juex", "extensions")
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
	if cfg.HomeExtensionsDir() != wantUserExtensions || cfg.ProjectExtensionsDir() != wantProjExtensions {
		t.Fatalf("extension dirs = home %q project %q", cfg.HomeExtensionsDir(), cfg.ProjectExtensionsDir())
	}
	runtimePaths := cfg.RuntimePaths()
	if runtimePaths.WorkDir != filepath.Join("/proj") || runtimePaths.MemoryDir != cfg.MemoryDir() || runtimePaths.HistoryPath != cfg.HistoryPath() {
		t.Fatalf("runtime paths = %+v", runtimePaths)
	}
	resourcePaths := cfg.ResourcePaths()
	if resourcePaths.ProjectAgentsDir != wantProjAgents || resourcePaths.HomeExtensionsDir != wantUserExtensions || resourcePaths.ProjectExtensionsDir != wantProjExtensions || len(resourcePaths.SkillDirs) != 2 || len(resourcePaths.MCPConfigPaths) != 2 {
		t.Fatalf("resource paths = %+v", resourcePaths)
	}
}

func TestPaths_EmptyWorkDirReturnsEmpty(t *testing.T) {
	cfg := Config{HomeAgentsDir: filepath.Join("/u", ".agents"), HomeJuexDir: filepath.Join("/u", ".juex"), EnableUserGlobalResources: true}
	if cfg.MemoryDir() != "" || cfg.SessionsDir() != "" || cfg.HistoryPath() != "" || cfg.RuntimeConfigPath() != "" || cfg.ProjectAgentsDir() != "" {
		t.Fatalf("empty WorkDir should yield empty work-local paths: %+v", cfg)
	}
	if cfg.ProjectExtensionsDir() != "" {
		t.Fatalf("empty WorkDir should yield empty project extension dir: %q", cfg.ProjectExtensionsDir())
	}
	if cfg.HomeRuntimeConfigPath() != filepath.Join("/u", ".juex", "juex.yaml") {
		t.Fatalf("home runtime config = %q", cfg.HomeRuntimeConfigPath())
	}
	if cfg.HomeExtensionsDir() != filepath.Join("/u", ".juex", "extensions") {
		t.Fatalf("home extension dir = %q", cfg.HomeExtensionsDir())
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

func TestPaths_DisabledUserGlobalResourcesOmitsHomeResources(t *testing.T) {
	cfg := Config{
		HomeAgentsDir:             filepath.Join("/u", ".agents"),
		HomeJuexDir:               filepath.Join("/u", ".juex"),
		WorkDir:                   filepath.Join("/proj"),
		EnableUserGlobalResources: false,
	}
	if cfg.GlobalAgentsMDPath() != "" {
		t.Fatalf("GlobalAgentsMDPath = %q, want empty", cfg.GlobalAgentsMDPath())
	}
	if cfg.HomeExtensionsDir() != "" {
		t.Fatalf("HomeExtensionsDir = %q, want empty", cfg.HomeExtensionsDir())
	}
	if cfg.ProjectExtensionsDir() != filepath.Join("/proj", ".juex", "extensions") {
		t.Fatalf("ProjectExtensionsDir = %q", cfg.ProjectExtensionsDir())
	}
	if got := cfg.SkillDirs(); len(got) != 1 || got[0] != filepath.Join("/proj", ".agents", "skills") {
		t.Fatalf("SkillDirs = %v", got)
	}
	if got := cfg.MCPConfigPaths(); len(got) != 1 || got[0] != filepath.Join("/proj", ".agents", "mcp.json") {
		t.Fatalf("MCPConfigPaths = %v", got)
	}
}

func TestProviderSelection_RequiresProviderSelector(t *testing.T) {
	cfg := Config{APIKey: "x", Model: "m"}
	if _, err := cfg.ProviderSelection().ProviderProfile(); err == nil {
		t.Fatal("expected error for empty provider selector")
	}
}

func TestRuntimeLimits_ResolvedValues(t *testing.T) {
	cfg := Config{
		ContextWindow: 1234,
		Compaction:    DefaultCompactionConfig(),
	}
	limits := cfg.RuntimeLimits()
	if limits.ContextWindow != 1234 {
		t.Fatalf("runtime limits = %+v", limits)
	}
	if !limits.Compaction.Enabled {
		t.Fatalf("compaction = %+v", limits.Compaction)
	}
}

func TestLoadFromFile_ThinkingEffort(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
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

func TestLoadFromFile_ThinkingEffortAllowedValues(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			prepareConfigTest(t)
			dir := t.TempDir()
			configPath := filepath.Join(dir, "juex.yaml")
			body := fmt.Sprintf(`model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
        thinking_effort: %s
`, effort)
			writeTextFile(t, configPath, body)

			cfg, err := LoadFromFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ThinkingEffort != effort {
				t.Fatalf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, effort)
			}
		})
	}
}

func TestLoadFromFile_TrimsThinkingEffort(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
        thinking_effort: " high "
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ThinkingEffort != "high" {
		t.Fatalf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, "high")
	}
}

func TestLoadFromFile_RejectsInvalidThinkingEffort(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
        thinking_effort: turbo
`
	writeTextFile(t, configPath, body)

	_, err := LoadFromFile(configPath)
	if err == nil {
		t.Fatal("expected invalid thinking_effort error")
	}
	if msg := err.Error(); !strings.Contains(msg, `invalid thinking_effort "turbo"`) || !strings.Contains(msg, allowedThinkingEffortText) {
		t.Fatalf("error = %q", msg)
	}
}

func TestLoadFromFile_TrimsThinkingEffortEnv(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")
	t.Setenv("PROVIDER_THINKING_EFFORT", " medium ")

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ThinkingEffort != "medium" {
		t.Fatalf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, "medium")
	}
}

func TestLoadFromFile_RejectsInvalidThinkingEffortEnv(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	writeJuexConfig(t, configPath, "openai", "https://example.com", "sk-x", "gpt-4")
	t.Setenv("PROVIDER_THINKING_EFFORT", "turbo")

	_, err := LoadFromFile(configPath)
	if err == nil {
		t.Fatal("expected invalid PROVIDER_THINKING_EFFORT error")
	}
	if msg := err.Error(); !strings.Contains(msg, "PROVIDER_THINKING_EFFORT") || !strings.Contains(msg, allowedThinkingEffortText) {
		t.Fatalf("error = %q", msg)
	}
}

func TestLoadFromFile_ContextWindow(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
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
	body := `model: openai:gpt-4
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

func TestLoadFromFile_LegacyRuntimeBudgetKeysAreIgnored(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
runtime:
  max_iters: 42
  max_duration: 15m
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.Model != "gpt-4" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadFromFile_PendingInputRuntimeTTL(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
runtime:
  pending_input_ttl: 30m
  external_event_ttl: 48h
  tool_timeout: 2m
  max_output_tokens: 8192
  working_state_enabled: false
  show_builtin_hook_traces: true
`
	writeTextFile(t, configPath, body)

	cfg, err := LoadFromFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	limits := cfg.RuntimeLimits()
	if limits.PendingInputTTL != 30*time.Minute || limits.ExternalEventTTL != 48*time.Hour || limits.ToolTimeout != 2*time.Minute {
		t.Fatalf("runtime TTLs = %+v", limits)
	}
	if limits.MaxOutputTokens != 8192 {
		t.Fatalf("runtime max output tokens = %d, want 8192", limits.MaxOutputTokens)
	}
	if limits.WorkingStateEnabled {
		t.Fatalf("working state should be disabled: %+v", limits)
	}
	if !limits.ShowBuiltinHookTraces {
		t.Fatalf("builtin hook traces should be enabled: %+v", limits)
	}
}

func TestLoadFromFile_InvalidPendingInputRuntimeTTL(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
runtime:
  pending_input_ttl: soon
`
	writeTextFile(t, configPath, body)

	_, err := LoadFromFile(configPath)
	if err == nil || !strings.Contains(err.Error(), "pending_input_ttl") {
		t.Fatalf("err = %v, want pending_input_ttl parse error", err)
	}
}

func TestLoadFromFile_InvalidToolTimeout(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-4
providers:
  - id: openai
    base_url: https://example.com
    api_key: sk-x
    models:
      - id: gpt-4
runtime:
  tool_timeout: soon
`
	writeTextFile(t, configPath, body)

	_, err := LoadFromFile(configPath)
	if err == nil || !strings.Contains(err.Error(), "tool_timeout") {
		t.Fatalf("err = %v, want tool_timeout parse error", err)
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
	body := `model: deepseek:deepseek-chat
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
      vision: false
      reasoning_replay: true
    compat:
      reasoning_replay_fields:
        - reasoning_content
      codex_transport: auto
    models:
      - id: deepseek-chat
        context_window: 64000
        headers:
          X-Model: deepseek-chat
        capabilities:
          vision: true
          max_output_tokens: false
        compat:
          codex_transport: websocket-cached
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
	if cfg.ProviderCapabilities.Vision == nil || !*cfg.ProviderCapabilities.Vision {
		t.Fatalf("vision override = %+v, want true", cfg.ProviderCapabilities.Vision)
	}
	if got := cfg.ProviderCompat.ReasoningReplayFields; len(got) != 1 || got[0] != "reasoning_content" {
		t.Fatalf("compat = %+v", cfg.ProviderCompat)
	}
	if cfg.ProviderCompat.CodexTransport != "websocket-cached" {
		t.Fatalf("codex transport = %q", cfg.ProviderCompat.CodexTransport)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "deepseek" || profile.Protocol != "openai/chat" || profile.Capabilities.Tools || !profile.Capabilities.Vision || profile.Capabilities.MaxOutputTokens || profile.Compat.CodexTransport != "websocket-cached" {
		t.Fatalf("profile = %+v", profile)
	}
	selection := cfg.ProviderSelection()
	if selection.ID != "deepseek" || selection.Model != "deepseek-chat" || selection.Headers["X-Model"] != "deepseek-chat" {
		t.Fatalf("provider selection = %+v", selection)
	}
	selectedProfile, err := selection.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if selectedProfile.ID != profile.ID || selectedProfile.Protocol != profile.Protocol || selectedProfile.Model != profile.Model {
		t.Fatalf("selected profile = %+v, want %+v", selectedProfile, profile)
	}
}

func TestLoadFromFile_ProviderCompatRejectsInvalidCodexTransport(t *testing.T) {
	prepareConfigTest(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "juex.yaml")
	body := `model: openai:gpt-test
providers:
  - id: openai
    api_key: sk-x
    compat:
      codex_transport: sideways
    models:
      - id: gpt-test
`
	writeTextFile(t, configPath, body)

	_, err := LoadFromFile(configPath)
	if err == nil || !strings.Contains(err.Error(), "unsupported codex transport") {
		t.Fatalf("err = %v, want invalid codex transport", err)
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
	body := `model: local-proxy:custom-model
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
	body := "model: " + id + ":" + model + "\n" +
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
	body := "model: openai-codex:gpt-test\n" +
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

func quoteYAMLString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
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
