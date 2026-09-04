package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/providerreadiness"
	"github.com/juex-ai/juex/internal/sandbox"
	toolruntime "github.com/juex-ai/juex/internal/tools"
	"github.com/juex-ai/juex/internal/version"
	"github.com/juex-ai/juex/internal/web"
)

func TestVersionCmd_ShortForm(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), version.String()) {
		t.Fatalf("got %q", out.String())
	}
}

func TestRootVersionFlagsMatchVersionSubcommand(t *testing.T) {
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("juex %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String()
	}

	want := run(t, "version")
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		if got := run(t, args...); got != want {
			t.Errorf("juex %s output = %q, want %q", strings.Join(args, " "), got, want)
		}
	}
}

func TestRootVersionFlagIsLocalAndDiscoverable(t *testing.T) {
	root := newRootCmd()
	flag := root.LocalFlags().Lookup("version")
	if flag == nil {
		t.Fatal("root local --version flag is missing")
	}
	if flag.Shorthand != "v" {
		t.Fatalf("--version shorthand = %q, want v", flag.Shorthand)
	}
	if root.PersistentFlags().Lookup("version") != nil {
		t.Fatal("--version must remain root-local so version -v keeps its meaning")
	}

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "-v, --version") {
		t.Fatalf("root help missing version aliases:\n%s", out.String())
	}
}

func TestVersionCmd_VerboseForm(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "-v"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"juex", "commit:", "built:", "go:", "os/arch:"} {
		if !strings.Contains(body, want) {
			t.Errorf("verbose missing %q in:\n%s", want, body)
		}
	}
}

func TestRootHelpGroupsSubcommandsByScope(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		"Managed resources",
		"Administration",
		"About this CLI",
		"agent",
		"thread",
		"config",
		"diagnose",
		"fleet",
		"version",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("help missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Additional Commands:") {
		t.Fatalf("help contains ungrouped commands:\n%s", body)
	}

	wantGroups := []cobra.Group{
		{ID: "resources", Title: "Managed resources"},
		{ID: "administration", Title: "Administration"},
		{ID: "cli", Title: "About this CLI"},
	}
	groups := root.Groups()
	if len(groups) != len(wantGroups) {
		t.Fatalf("root groups = %+v, want %+v", groups, wantGroups)
	}
	for i, want := range wantGroups {
		if *groups[i] != want {
			t.Errorf("root group[%d] = %+v, want %+v", i, *groups[i], want)
		}
	}

	wantCommandGroups := map[string]string{
		"agent":      "resources",
		"completion": "cli",
		"config":     "administration",
		"diagnose":   "administration",
		"fleet":      "administration",
		"help":       "cli",
		"listen":     "administration",
		"thread":     "resources",
		"version":    "cli",
	}
	commands := root.Commands()
	if len(commands) != len(wantCommandGroups) {
		t.Fatalf("root commands = %d, want %d: %+v", len(commands), len(wantCommandGroups), commands)
	}
	for _, command := range commands {
		if want, ok := wantCommandGroups[command.Name()]; !ok {
			t.Errorf("unexpected root command %q", command.Name())
		} else if command.GroupID != want {
			t.Errorf("%s GroupID = %q, want %q", command.Name(), command.GroupID, want)
		}
	}
}

func TestRootLongNamesWorkspaceFleetAndCLIScopes(t *testing.T) {
	body := newRootCmd().Long
	for _, want := range []string{
		"Fleet manages",
		"Agent commands",
		"Thread commands",
		"selected Agent Runtime",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("root Long missing %q:\n%s", want, body)
		}
	}
}

func TestUnknownSubcommandIsError(t *testing.T) {
	root := newRootCmd()
	root.SilenceUsage = true
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"totally-bogus"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestExecute_ZeroExitOnVersion(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionCmd_JSONForm(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{`"name": "juex"`, `"version":`, `"go_version":`, `"os":`, `"arch":`} {
		if !strings.Contains(body, want) {
			t.Errorf("json missing %q in:\n%s", want, body)
		}
	}
}

func TestVersionCmdDoesNotLoadRuntimeConfig(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	if err := writeTextFile(filepath.Join(work, ".juex", "juex.yaml"), ": malformed"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("version loaded malformed runtime config: %v", err)
	}
	if !strings.Contains(out.String(), `"name": "juex"`) {
		t.Fatalf("version output = %s", out.String())
	}
}

func TestDoctorDoesNotCreateAgentState(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	if err := writeJuexConfigFile(filepath.Join(work, ".juex", "juex.yaml"), "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})

	err := root.Execute()
	var doctorErr *doctorExitError
	if !errors.As(err, &doctorErr) || doctorErr.status != doctorStatusWarn {
		t.Fatalf("doctor err = %T %v, want warning\n%s", err, err, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("doctor stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), "no Agent is registered") {
		t.Fatalf("doctor output missing no-agent warning:\n%s", stdout.String())
	}
}

func TestDoctorInspectsExtensionDataDefaultsBeforeAgentCreation(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(configPath, "openai", "https://example.invalid", "sk-test", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	if err := appendTextFile(configPath, "extensions:\n  allow: [demo]\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(work, ".juex", "extensions", "demo", "juex.extension.json"), `{
  "manifest_version":1,
  "name":"demo",
  "version":"1.0.0",
  "agent":{"environment":{"variables":{"DOCTOR_DATA":"${JUEX_EXT_DATA_DIR}"}}}
}`); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})
	err := root.Execute()
	var doctorErr *doctorExitError
	if !errors.As(err, &doctorErr) || doctorErr.status != doctorStatusWarn {
		t.Fatalf("doctor execute: %T %v, want no-Agent warning\n%s", err, err, out.String())
	}
	var result doctorResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, out.String())
	}
	checks := map[string]doctorCheck{}
	for _, check := range result.Checks {
		checks[check.Name] = check
	}
	if checks["agent"].Status != doctorStatusWarn || checks["environment"].Status != doctorStatusOK || checks["mcp"].Status == doctorStatusFail {
		t.Fatalf("doctor checks = %+v", checks)
	}
	if checks["environment"].Details["extension_default_count"] != float64(1) {
		t.Fatalf("environment details = %+v", checks["environment"].Details)
	}
}

func TestDoctorAgentCheckReportsUnregisteredWorkspace(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()

	check := doctorAgentCheck(work)

	if check.Status != doctorStatusWarn {
		t.Fatalf("status = %q, want %q", check.Status, doctorStatusWarn)
	}
	if !strings.Contains(check.Message, "no Agent is registered") {
		t.Fatalf("message = %q, want no-agent explanation", check.Message)
	}
	const want = "run juex agent add <workspace> to register this Workspace"
	if check.Suggestion != want {
		t.Fatalf("suggestion = %q, want %q", check.Suggestion, want)
	}
}

func TestLoadRuntimeConfigForCommandActivatesAndRestoresEnvironment(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	path := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(path, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	if err := appendTextFile(path, "environment:\n  variables:\n    JUEX_RUNTIME_ACTIVATION_TEST: configured\n"); err != nil {
		t.Fatal(err)
	}
	cfg := ensureTestWorkspaceAgent(t, work)
	original, originallySet := os.LookupEnv("JUEX_RUNTIME_ACTIVATION_TEST")
	if err := os.Unsetenv("JUEX_RUNTIME_ACTIVATION_TEST"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if originallySet {
			_ = os.Setenv("JUEX_RUNTIME_ACTIVATION_TEST", original)
		} else {
			_ = os.Unsetenv("JUEX_RUNTIME_ACTIVATION_TEST")
		}
	})

	root := newRootCmd()
	listenCmd, _, err := root.Find([]string{"listen"})
	if err != nil {
		t.Fatal(err)
	}
	_, lifecycle, err := loadRuntimeConfigForCommand(listenCmd, &persistentFlags{agentID: cfg.AgentID})
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("JUEX_RUNTIME_ACTIVATION_TEST"); got != "configured" {
		t.Fatalf("activated environment = %q", got)
	}
	if err := lifecycle.finish(listenCmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("JUEX_RUNTIME_ACTIVATION_TEST"); ok {
		t.Fatal("runtime environment was not restored")
	}
}

func TestInitCmd_NonInteractiveWorkspaceWritesConfig(t *testing.T) {
	setHomeForCLITest(t)
	root := newRootCmd()
	var out bytes.Buffer
	work := t.TempDir()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"config", "init",
		"--cwd", work,
		"--scope", "workspace",
		"--provider", "openai",
		"--model", "gpt-4.1",
		"--api-key", "sk-test",
		"--base-url", "https://openai.example",
		"--skip-check",
		"--yes",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("init execute: %v\n%s", err, out.String())
	}

	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"models:", "openai:gpt-4.1", "id: openai", "base_url: https://openai.example", "api_key: sk-test", "id: gpt-4.1"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{WorkDir: work, AgentState: config.AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.Model != "gpt-4.1" || cfg.APIKey != "sk-test" {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, want := range []string{
		fmt.Sprintf("juex agent add %q", work),
		fmt.Sprintf(`juex agent send --cwd %q --wait "say hello"`, work),
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("quickstart missing %q from output:\n%s", want, out.String())
		}
	}
}

func TestInitHelloCheckErrorIncludesSuggestion(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	err := initHelloCheckError(providerreadiness.Result{
		Message:    "provider hello check failed",
		Suggestion: "check network connectivity and provider credentials",
		Err:        cause,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("expected wrapped cause, got %v", err)
	}
	if !strings.Contains(err.Error(), "check network connectivity and provider credentials") {
		t.Fatalf("expected suggestion in error, got %q", err.Error())
	}
}

func TestDiagnoseConfigValidatesAdHocFileAtExplicitScope(t *testing.T) {
	setHomeForCLITest(t)
	work := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "explicit.yaml")
	if err := writeTextFile(explicit, "extensions:\n  allow: [demo]\n"); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--config", explicit, "--offline", "--format", "json"})
	err := root.Execute()
	var doctorErr *doctorExitError
	if !errors.As(err, &doctorErr) || doctorErr.status != doctorStatusFail {
		t.Fatalf("error = %T %v, want failed diagnose; output=%s", err, err, out.String())
	}
	if !strings.Contains(out.String(), "extensions.allow") || !strings.Contains(out.String(), "workspace config") {
		t.Fatalf("diagnose did not preserve explicit config scope:\n%s", out.String())
	}
}

func TestInitTargetPathUsesJUEXHome(t *testing.T) {
	home := setHomeForCLITest(t)
	juexHome := filepath.Join(home, "alternate-home")
	t.Setenv("JUEX_HOME", juexHome)

	got, err := initTargetPath("user", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(juexHome, "juex.yaml"); got != want {
		t.Fatalf("init target = %q, want %q", got, want)
	}
}

func TestInitCmd_MergesExistingProviderWithoutOverwriting(t *testing.T) {
	setHomeForCLITest(t)
	root := newRootCmd()
	var out bytes.Buffer
	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(configPath, "openai", "https://old.example", "sk-old", "gpt-old"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(work, ".juex", "shared.yaml"), "runtime:\n  tool_timeout: 45s\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(configPath, "imports:\n  - source: shared.yaml\n"+string(original)); err != nil {
		t.Fatal(err)
	}
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"config", "init",
		"--cwd", work,
		"--scope", "workspace",
		"--provider", "openai",
		"--model", "gpt-new",
		"--api-key", "sk-new",
		"--base-url", "https://new.example",
		"--skip-check",
		"--yes",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("init execute: %v\n%s", err, out.String())
	}

	bodyBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	for _, want := range []string{"imports:", "source: shared.yaml", "models:", "openai:gpt-old", "base_url: https://old.example", "api_key: sk-old", "id: gpt-new"} {
		if !strings.Contains(body, want) {
			t.Fatalf("merged config missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"https://new.example", "sk-new"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("merge should not overwrite existing provider with %q:\n%s", forbidden, body)
		}
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir: work, ModelRefs: []string{"openai:gpt-new"}, AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://old.example" || cfg.APIKey != "sk-old" || cfg.Model != "gpt-new" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestMergeInitConfigFileFillsMissingProviderFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "juex.yaml")
	if err := writeTextFile(path, `models: [openai:gpt-4.1]
providers:
  - id: openai
    models:
      - id: gpt-4.1
`); err != nil {
		t.Fatal(err)
	}

	_, err := mergeInitConfigFile(path, initProviderSpec{
		ID:       "openai",
		Protocol: "openai/chat",
		BaseURL:  "https://new.example",
		APIKey:   "sk-new",
		Model:    "gpt-4.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	for _, want := range []string{"protocol: openai/chat", "base_url: https://new.example", "api_key: sk-new"} {
		if !strings.Contains(body, want) {
			t.Fatalf("merged config missing %q:\n%s", want, body)
		}
	}
	if got := strings.Count(body, "id: gpt-4.1"); got != 1 {
		t.Fatalf("model entries = %d, want 1:\n%s", got, body)
	}
}

func TestMergeInitConfigFileTightensSecretFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file mode bits do not model Unix secret permissions")
	}
	path := filepath.Join(t.TempDir(), "juex.yaml")
	if err := writeJuexConfigFile(path, "openai", "https://old.example", "sk-old", "gpt-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := mergeInitConfigFile(path, initProviderSpec{
		ID:     "openai",
		APIKey: "sk-new",
		Model:  "gpt-new",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

func TestInitCmd_UserScopeIgnoresBrokenWorkspaceConfig(t *testing.T) {
	home := setHomeForCLITest(t)
	root := newRootCmd()
	var out bytes.Buffer
	work := t.TempDir()
	if err := writeTextFile(filepath.Join(work, ".juex", "juex.yaml"), "models: [broken\n"); err != nil {
		t.Fatal(err)
	}
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"config", "init",
		"--cwd", work,
		"--scope", "user",
		"--provider", "openai",
		"--model", "gpt-4.1",
		"--api-key", "sk-user",
		"--skip-check",
		"--yes",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("user-scope init should ignore broken workspace config: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".juex", "juex.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInitConfigForCheckIgnoresBrokenWorkspaceConfig(t *testing.T) {
	home := setHomeForCLITest(t)
	work := t.TempDir()
	if err := writeTextFile(filepath.Join(work, ".juex", "juex.yaml"), "models: [broken\n"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(target, "openai", "https://example.invalid", "sk-user", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadInitConfigForCheck(target, work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderID != "openai" || cfg.Model != "gpt-4.1" || cfg.APIKey != "sk-user" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestValidateInitConfigTreatsUserTargetAsUserConfig(t *testing.T) {
	home := setHomeForCLITest(t)
	work := t.TempDir()
	target := filepath.Join(home, ".juex", "juex.yaml")
	if err := writeTextFile(target, `models: [openai:gpt-4.1]
providers:
  - id: openai
    base_url: https://example.invalid
    api_key: sk-user
    models:
      - id: gpt-4.1
hooks:
  commands:
    - name: global-context
      events: [UserPromptSubmit]
      command: ["echo", "{}"]
`); err != nil {
		t.Fatal(err)
	}

	if err := validateInitConfig(target, work); err != nil {
		t.Fatalf("user-scope init validation should not require hooks.trusted: true: %v", err)
	}
	cfg, err := loadInitConfigForCheck(target, work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.Commands) != 1 || cfg.Hooks.Commands[0].Name != "global-context" || cfg.Hooks.Commands[0].Source != "home:default" {
		t.Fatalf("hooks = %+v", cfg.Hooks.Commands)
	}
}

func TestInitCmdUserScopeWritesOnlyEffectiveInstanceHome(t *testing.T) {
	home := setHomeForCLITest(t)
	defaultHome := filepath.Join(home, ".juex")
	if err := os.MkdirAll(defaultHome, 0o755); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(defaultHome, "juex.yaml")
	defaultBody := []byte("models: [shared:base]\nproviders:\n  - id: shared\n    protocol: openai/chat\n    api_key: shared-key\n    models:\n      - id: base\n")
	if err := os.WriteFile(defaultPath, defaultBody, 0o640); err != nil {
		t.Fatal(err)
	}
	instanceHome := filepath.Join(home, "instance")
	t.Setenv("JUEX_HOME", instanceHome)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"config", "init",
		"--scope", "user",
		"--provider", "openai",
		"--model", "gpt-4.1",
		"--api-key", "sk-instance",
		"--skip-check",
		"--yes",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}
	instancePath := filepath.Join(instanceHome, "juex.yaml")
	if _, err := os.Stat(instancePath); err != nil {
		t.Fatalf("instance config: %v", err)
	}
	gotDefault, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDefault, defaultBody) {
		t.Fatalf("default config changed:\n%s", gotDefault)
	}
	info, err := os.Stat(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("default config mode = %o, want unchanged 640", info.Mode().Perm())
	}
}

func TestValidateInitConfigTreatsJUEXHomeTargetAsUserConfig(t *testing.T) {
	home := setHomeForCLITest(t)
	work := t.TempDir()
	juexHome := filepath.Join(home, "custom-juex")
	t.Setenv("JUEX_HOME", juexHome)
	target := filepath.Join(juexHome, "juex.yaml")
	if err := writeTextFile(target, `models: [openai:gpt-4.1]
providers:
  - id: openai
    base_url: https://example.invalid
    api_key: sk-user
    models:
      - id: gpt-4.1
hooks:
  commands:
    - name: global-context
      events: [UserPromptSubmit]
      command: ["echo", "{}"]
`); err != nil {
		t.Fatal(err)
	}

	if got := initConfigTargetScope(target, work); got != "user" {
		t.Fatalf("target scope = %q, want user", got)
	}
	if err := validateInitConfig(target, work); err != nil {
		t.Fatalf("JUEX_HOME user config validation should trust user hooks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(juexHome, "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init validation created an agent registry: %v", err)
	}
}

func TestDoctorCmd_JSONOfflineValidConfig(t *testing.T) {
	setHomeForCLITest(t)
	root := newRootCmd()
	var out bytes.Buffer
	work := t.TempDir()
	if err := writeJuexConfigFile(filepath.Join(work, ".juex", "juex.yaml"), "openai", "https://example.invalid", "sk-test", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	const environmentSecret = "doctor-environment-secret-sentinel"
	if err := writeTextFile(filepath.Join(work, ".env"), "DOCTOR_ENV_SECRET="+environmentSecret+"\n"); err != nil {
		t.Fatal(err)
	}
	mcpConfig, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"self": map[string]any{"command": os.Args[0]},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(work, ".agents", "mcp.json"), string(mcpConfig)); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(work, ".agents", "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: demo skill\n---\nbody\n"); err != nil {
		t.Fatal(err)
	}
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})
	err = root.Execute()
	var doctorErr *doctorExitError
	if !errors.As(err, &doctorErr) || doctorErr.status != doctorStatusWarn {
		t.Fatalf("doctor execute: %T %v, want warning\n%s", err, err, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, out.String())
	}
	if result["status"] != "warn" {
		t.Fatalf("status = %#v, want warn:\n%s", result["status"], out.String())
	}
	checks, _ := result["checks"].([]any)
	seen := map[string]bool{}
	var skillsMessage string
	for _, raw := range checks {
		row, _ := raw.(map[string]any)
		seen[row["name"].(string)] = true
		if row["name"] == "skills" {
			skillsMessage, _ = row["message"].(string)
		}
	}
	for _, want := range []string{"config", "environment", "credentials", "connectivity", "shell", "ripgrep", "workdir", "mcp", "skills"} {
		if !seen[want] {
			t.Fatalf("missing check %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), environmentSecret) {
		t.Fatalf("doctor leaked environment value:\n%s", out.String())
	}
	for _, want := range []string{"DOCTOR_ENV_SECRET", `"source": "dotenv"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor environment metadata missing %q:\n%s", want, out.String())
		}
	}
	if skillsMessage != "4 skill(s) loaded" {
		t.Fatalf("skills doctor message = %q, want project plus three builtin guides", skillsMessage)
	}
}

func TestDoctorCmd_ReportsExtensionEnvironmentWithoutValues(t *testing.T) {
	const secretDefault = "doctor-extension-default-secret"
	home := setHomeForCLITest(t)
	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(configPath, "openai", "https://example.invalid", "sk-test", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	if err := appendTextFile(configPath, "extensions:\n  allow: [demo]\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentstate.Resolve(agentstate.Options{HomeDir: filepath.Join(home, ".juex"), WorkDir: work}); err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(work, ".juex", "extensions", "demo")
	if err := writeTextFile(filepath.Join(extensionDir, "juex.extension.json"), `{
  "manifest_version":1,
  "name":"demo",
  "version":"1.0.0",
  "agent":{"environment":{"variables":{
    "DOCTOR_EXTENSION_DEFAULT":"`+secretDefault+`",
	"DOCTOR_EXTENSION_DIR":"${JUEX_EXT_DIR}",
    "DOCTOR_EXTENSION_DATA":"${JUEX_EXT_DATA_DIR}"
  }}}
}`); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})
	err := root.Execute()
	var doctorErr *doctorExitError
	if err != nil && !errors.As(err, &doctorErr) {
		t.Fatalf("doctor execute: %T %v\n%s", err, err, out.String())
	}
	if strings.Contains(out.String(), secretDefault) {
		t.Fatalf("doctor leaked Extension environment value:\n%s", out.String())
	}
	for _, want := range []string{
		`"extension_default_count": 3`,
		`"key": "DOCTOR_EXTENSION_DEFAULT"`,
		`"key": "DOCTOR_EXTENSION_DIR"`,
		`"key": "DOCTOR_EXTENSION_DATA"`,
		`"source": "ext:demo"`,
		`"status": "effective"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor Extension environment metadata missing %q:\n%s", want, out.String())
		}
	}
	var result doctorResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, out.String())
	}
	wantManifestPath := filepath.Join(extensionDir, "juex.extension.json")
	foundExtensionDir := false
	for _, check := range result.Checks {
		if check.Name != "environment" {
			continue
		}
		rows, _ := check.Details["extension_default_variables"].([]any)
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			if row["key"] == "DOCTOR_EXTENSION_DIR" {
				foundExtensionDir = true
				if row["path"] != wantManifestPath {
					t.Fatalf("doctor Extension provenance path = %q, want %q", row["path"], wantManifestPath)
				}
			}
		}
	}
	if !foundExtensionDir {
		t.Fatalf("doctor Extension provenance row not found: %s", out.String())
	}
}

func TestDoctorConfigCheckDistinguishesDefaultAndInstanceHomePaths(t *testing.T) {
	home := setHomeForCLITest(t)
	for _, key := range []string{
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
	} {
		t.Setenv(key, "")
	}
	defaultPath := filepath.Join(home, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(defaultPath, "openai", "https://example.invalid", "sk-shared", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	instanceHome := filepath.Join(home, "instance")
	if err := os.MkdirAll(instanceHome, 0o755); err != nil {
		t.Fatal(err)
	}
	instancePath := filepath.Join(instanceHome, "juex.yaml")
	if err := writeTextFile(instancePath, "models: [openai:gpt-4.1]\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUEX_HOME", instanceHome)
	defaultPath, err := filepath.EvalSymlinks(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	instancePath, err = filepath.EvalSymlinks(instancePath)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadForWorkDirForValidation(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	check := doctorConfigCheck(cfg)
	if check.Status != doctorStatusOK {
		t.Fatalf("check = %+v", check)
	}
	if got := check.Details["default_home_config_path"]; got != defaultPath {
		t.Fatalf("default home config path = %#v, want %q", got, defaultPath)
	}
	if got := check.Details["effective_home_config_path"]; got != instancePath {
		t.Fatalf("effective home config path = %#v, want %q", got, instancePath)
	}
}

func TestDoctorConfigCheckReportsImportFreshnessWithoutSecrets(t *testing.T) {
	home := setHomeForCLITest(t)
	for _, key := range []string{
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
	} {
		t.Setenv(key, "")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`models: [local:test]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://example.invalid
    api_key: imported-api-secret
    models:
      - id: test
`))
	}))
	configPath := filepath.Join(home, ".juex", "juex.yaml")
	if err := writeTextFile(configPath, "imports:\n  - source: "+server.URL+"/config.yaml?token=url-secret\n"); err != nil {
		t.Fatal(err)
	}

	workDir := t.TempDir()
	if _, err := config.LoadForWorkDirForValidation(workDir); err != nil {
		t.Fatal(err)
	}
	server.Close()
	cfg, err := config.LoadForWorkDirForValidation(workDir)
	if err != nil {
		t.Fatal(err)
	}
	check := doctorConfigCheck(cfg)
	if check.Status != doctorStatusWarn || !strings.Contains(check.Message, "stale") {
		t.Fatalf("check = %+v, want stale import warning", check)
	}
	imports, ok := check.Details["config_imports"].([]map[string]any)
	if !ok || len(imports) != 1 || imports[0]["state"] != "stale" || imports[0]["digest"] == "" {
		t.Fatalf("config imports = %#v", check.Details["config_imports"])
	}
	encoded, err := json.Marshal(check)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"imported-api-secret", "url-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("doctor output leaked %q: %s", secret, encoded)
		}
	}
}

func TestDoctorConnectivityCheckActivatesRuntimeEnvironmentForProbe(t *testing.T) {
	const proxy = "http://127.0.0.1:18765"
	setHomeForCLITest(t)
	previous, existed := os.LookupEnv("HTTP_PROXY")
	if err := os.Unsetenv("HTTP_PROXY"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("HTTP_PROXY", previous)
		} else {
			_ = os.Unsetenv("HTTP_PROXY")
		}
	})

	work := t.TempDir()
	if err := writeJuexConfigFile(
		filepath.Join(work, ".juex", "juex.yaml"),
		"openai",
		"https://example.invalid",
		"sk-test",
		"gpt-4.1",
	); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(work, ".env"), "HTTP_PROXY="+proxy+"\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    work,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	check := doctorConnectivityCheckWithOptions(
		context.Background(),
		cfg,
		providerreadiness.ConnectivityOptions{Probe: providerreadiness.ProbeFunc(
			func(_ context.Context, _ llm.ProviderProfile) error {
				if got := os.Getenv("HTTP_PROXY"); got != proxy {
					return errors.New("probe did not receive resolved HTTP_PROXY")
				}
				return nil
			},
		)},
	)
	if check.Status != doctorStatusOK {
		t.Fatalf("connectivity check = %+v, want ok", check)
	}
	if _, ok := os.LookupEnv("HTTP_PROXY"); ok {
		t.Fatal("doctor connectivity check did not restore HTTP_PROXY")
	}
}

func TestDoctorCmdReportsMalformedDotenvWithoutPartialValues(t *testing.T) {
	setHomeForCLITest(t)
	root := newRootCmd()
	var out bytes.Buffer
	work := t.TempDir()
	if err := writeJuexConfigFile(filepath.Join(work, ".juex", "juex.yaml"), "openai", "https://example.invalid", "sk-test", "gpt-4.1"); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(work, ".env"), "SAFE_BEFORE=must-not-leak\nNOT_AN_ASSIGNMENT\n"); err != nil {
		t.Fatal(err)
	}
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})
	err := root.Execute()
	var doctorErr *doctorExitError
	if !errors.As(err, &doctorErr) || doctorErr.status != doctorStatusFail {
		t.Fatalf("doctor execute: %T %v, want failure\n%s", err, err, out.String())
	}
	if !strings.Contains(out.String(), ".env line 2") {
		t.Fatalf("doctor did not report malformed dotenv:\n%s", out.String())
	}
	if strings.Contains(out.String(), "must-not-leak") {
		t.Fatalf("doctor leaked partial dotenv value:\n%s", out.String())
	}
}

func TestDoctorCredentialsCheckWarnsForLocalOrCustomProvidersWithoutAPIKey(t *testing.T) {
	local := doctorCredentialsCheck(config.Config{
		ProviderID:       "openai",
		ProviderProtocol: string(llm.ProtocolOpenAIChat),
		BaseURL:          "http://127.0.0.1:11434/v1",
		Model:            "local-model",
	})
	if local.Status != doctorStatusWarn {
		t.Fatalf("local status = %s, want warn", local.Status)
	}

	custom := doctorCredentialsCheck(config.Config{
		ProviderID:       "local-proxy",
		ProviderProtocol: string(llm.ProtocolOpenAIChat),
		BaseURL:          "https://proxy.example",
		Model:            "model",
	})
	if custom.Status != doctorStatusWarn {
		t.Fatalf("custom status = %s, want warn", custom.Status)
	}

	cloud := doctorCredentialsCheck(config.Config{
		ProviderID:       "openai",
		ProviderProtocol: string(llm.ProtocolOpenAIResponses),
		Model:            "gpt-4.1",
	})
	if cloud.Status != doctorStatusFail {
		t.Fatalf("cloud status = %s, want fail", cloud.Status)
	}
}

func TestDoctorWorkdirCheckMissingJuexIsHealthy(t *testing.T) {
	check := doctorWorkdirCheck(t.TempDir())
	if check.Status != doctorStatusOK {
		t.Fatalf("workdir status = %s, want ok: %+v", check.Status, check)
	}
}

func TestDoctorSandboxCheckReportsDisabledAndUnavailable(t *testing.T) {
	disabled := doctorSandboxCheck(context.Background(), sandbox.DisabledPolicy(), "windows", nil)
	if disabled.Status != doctorStatusOK || disabled.Details["enabled"] != false {
		t.Fatalf("disabled check = %+v", disabled)
	}

	enabled := sandbox.DefaultPolicyForOS("linux")
	missing := doctorSandboxCheck(context.Background(), enabled, "linux", func(string) (string, error) {
		return "", errors.New("bwrap missing")
	})
	if missing.Status != doctorStatusFail || missing.Details["error_code"] != sandbox.ErrorCodeBackendUnavailable || !strings.Contains(missing.Suggestion, "bubblewrap") {
		t.Fatalf("missing check = %+v", missing)
	}

	unsupported := doctorSandboxCheck(context.Background(), sandbox.Policy{Enabled: true}, "windows", nil)
	if unsupported.Status != doctorStatusFail || unsupported.Details["error_code"] != sandbox.ErrorCodeUnsupportedPlatform {
		t.Fatalf("unsupported check = %+v", unsupported)
	}
}

func TestDoctorRipgrepCheckReportsResolvedRuntime(t *testing.T) {
	check := doctorRipgrepCheck(func() (toolruntime.ResolvedRipgrep, error) {
		return toolruntime.ResolvedRipgrep{
			Path:    "/managed/juex-path/rg",
			Version: "15.1.0",
			Source:  toolruntime.RipgrepSourcePackage,
		}, nil
	})
	if check.Status != doctorStatusOK || check.Name != "ripgrep" {
		t.Fatalf("check = %+v", check)
	}
	if check.Details["path"] != "/managed/juex-path/rg" || check.Details["version"] != "15.1.0" || check.Details["source"] != "package" {
		t.Fatalf("details = %+v", check.Details)
	}

	missing := doctorRipgrepCheck(func() (toolruntime.ResolvedRipgrep, error) {
		return toolruntime.ResolvedRipgrep{}, errors.New("not found")
	})
	if missing.Status != doctorStatusWarn || !strings.Contains(missing.Suggestion, "release package") {
		t.Fatalf("missing check = %+v", missing)
	}
}

func TestDoctorCmd_JSONOfflineEmptyConfigFailsWithInitSuggestion(t *testing.T) {
	setHomeForCLITest(t)
	root := newRootCmd()
	var out bytes.Buffer
	work := t.TempDir()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diagnose", "--cwd", work, "--format", "json", "--offline"})
	err := root.Execute()
	var doctorErr *doctorExitError
	if !errors.As(err, &doctorErr) {
		t.Fatalf("err = %T %v, want doctorExitError; stdout=%s", err, err, out.String())
	}
	if doctorErr.status != doctorStatusFail {
		t.Fatalf("doctor status = %q", doctorErr.status)
	}
	for _, want := range []string{`"status": "fail"`, "juex config init"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestExitCodes_DistinctTypes(t *testing.T) {
	// Quick sanity that each error type maps to its dedicated exit code via
	// the type switch in Execute(). We can't call Execute() directly because
	// it builds its own root cmd from scratch, but we can verify the switch.
	cases := map[error]int{
		&usageError{msg: "u"}:      ExitUsageError,
		&notFoundError{msg: "n"}:   ExitNotFound,
		&permissionError{msg: "p"}: ExitPermission,
		&conflictError{msg: "c"}:   ExitConflict,
		&dryRunOK{msg: "d"}:        ExitDryRun,
	}
	for err, want := range cases {
		got := classifyForTest(err)
		if got != want {
			t.Errorf("err %T -> %d, want %d", err, got, want)
		}
	}
	if classifyForTest(nil) != ExitSuccess {
		t.Error("nil err should be ExitSuccess")
	}
	if classifyForTest(&strErr{"foo"}) != ExitGeneralError {
		t.Error("unknown err type should be ExitGeneralError")
	}
}

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }

// classifyForTest mirrors Execute()'s type switch but skips the printing.
func classifyForTest(err error) int {
	if err == nil {
		return ExitSuccess
	}
	switch e := err.(type) {
	case *dryRunOK:
		return ExitDryRun
	case *doctorExitError:
		return e.ExitCode()
	case *usageError:
		return ExitUsageError
	case *notFoundError:
		return ExitNotFound
	case *permissionError:
		return ExitPermission
	case *conflictError:
		return ExitConflict
	default:
		return ExitGeneralError
	}
}

func writeJuexConfigFile(path, id, base, key, model string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "models: [" + id + ":" + model + "]\n" +
		"providers:\n" +
		"  - id: " + id + "\n" +
		"    base_url: " + base + "\n" +
		"    api_key: " + key + "\n" +
		"    models:\n" +
		"      - id: " + model + "\n"
	return os.WriteFile(path, []byte(body), 0o644)
}

func appendTextFile(path, body string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(body)
	return err
}

func writeTextFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func setHomeForCLITest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CODEX_HOME", filepath.Join(home, "missing-codex-home"))
	return home
}

func TestReportListenReadyIncludesEndpointSchemeAndFallback(t *testing.T) {
	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	reportListenReady(cmd, web.ReadyInfo{
		AgentEndpoint:  "tcp://127.0.0.1:43123",
		TCPAddress:     "127.0.0.1:8080",
		FallbackReason: "unix sockets unsupported",
	})
	for _, want := range []string{
		"tcp://127.0.0.1:43123",
		"juex listen agent JSON/SSE API (no web UI) listening on http://127.0.0.1:8080",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	for _, want := range []string{"WARNING", "unix sockets unsupported", "tcp://127.0.0.1:43123"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.42.0.99:8080", true}, // anywhere in 127.0.0.0/8
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{"localhost", true}, // bare host
		{"0.0.0.0:8080", false},
		{"192.168.1.5:8080", false},
		{"10.0.0.1:8080", false},
		{"", false},
		{"not-an-address", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
