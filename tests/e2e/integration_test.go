//go:build integration

// Live integration smoke tests. Build-tag gated so they only run when
// explicitly opted in (no API key in normal CI). Reads provider configuration
// from JUEX_PROVIDER_CONFIG or ~/.juex/juex.yaml.
//
//	go test -tags=integration ./tests/e2e/... -count=1 -v -run '^TestLiveConfigs_'
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/tools"
)

var liveConfigSelectorEnvKeys = []string{
	"PROVIDER_API_ID",
	"PROVIDER_API_PROTOCOL",
	"PROVIDER_API_MODEL",
}

const (
	liveProviderConfigEnv = "JUEX_PROVIDER_CONFIG"
	liveProviderModelEnv  = "JUEX_PROVIDER_SMOKE_ONLY"
)

type liveConfig struct {
	name string
	path string
	cfg  config.Config
}

// repoRoot walks up from the test file location until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, _ := os.Getwd()
	dir := cwd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root from %s", cwd)
	return ""
}

func loadLiveConfigs(t *testing.T) []liveConfig {
	t.Helper()
	root := repoRoot(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home for live provider config: %v", err)
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	configuredPath := strings.TrimSpace(os.Getenv(liveProviderConfigEnv))
	path := resolveLiveProviderConfigPath(root, home, configuredPath)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if liveProviderConfigMissingIsFatal(configuredPath) {
				t.Fatalf("%s points to missing live provider config %s", liveProviderConfigEnv, path)
			}
			t.Skipf(
				"live provider config %s not found; create ~/.juex/juex.yaml or set %s",
				path,
				liveProviderConfigEnv,
			)
		}
		t.Fatalf("check live provider config %s: %v", path, err)
	}
	workDir := t.TempDir()
	selectedPath, err := prepareLiveProviderConfig(root, path, workDir, os.Getenv(liveProviderModelEnv))
	if err != nil {
		t.Fatalf("load live provider config: %v", err)
	}

	runtimeUserHome := t.TempDir()
	juexHome := filepath.Join(runtimeUserHome, ".juex")
	t.Setenv("HOME", runtimeUserHome)
	t.Setenv("USERPROFILE", runtimeUserHome)
	t.Setenv("JUEX_HOME", juexHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(juexHome, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// Preserve Codex auth, credentials, and tuning overrides, but keep the
	// extracted provider:model selection and Juex home layers isolated.
	for _, k := range liveConfigSelectorEnvKeys {
		t.Setenv(k, "")
	}

	selected, err := loadPreparedLiveConfig(path, selectedPath, workDir)
	if err != nil {
		t.Fatalf("load live provider config: %v", err)
	}
	return []liveConfig{selected}
}

func liveProviderConfigMissingIsFatal(configuredPath string) bool {
	return configuredPath != ""
}

func resolveLiveProviderConfigPath(root, home, configured string) string {
	path := strings.TrimSpace(configured)
	if path == "" {
		return filepath.Join(home, ".juex", "juex.yaml")
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		path = filepath.Join(home, path[2:])
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

func loadLiveConfig(root, path, workDir, modelOverride string) (liveConfig, error) {
	selectedPath, err := prepareLiveProviderConfig(root, path, workDir, modelOverride)
	if err != nil {
		return liveConfig{}, err
	}
	return loadPreparedLiveConfig(path, selectedPath, workDir)
}

func prepareLiveProviderConfig(root, path, workDir, modelOverride string) (string, error) {
	modelRef := strings.TrimSpace(modelOverride)
	if modelRef != "" {
		if _, err := config.ParseModelRef(modelRef); err != nil {
			return "", fmt.Errorf("%s must be a complete provider:model for integration: %w", liveProviderModelEnv, err)
		}
	}
	selectedPath := filepath.Join(workDir, "selected-provider.juex.yaml")
	if err := writeLiveProviderConfig(root, path, selectedPath, modelRef); err != nil {
		return "", err
	}
	return selectedPath, nil
}

func loadPreparedLiveConfig(path, selectedPath, workDir string) (liveConfig, error) {
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    workDir,
		ConfigPath: selectedPath,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		return liveConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		return liveConfig{}, fmt.Errorf("%s: unusable provider %s:%s: %w", path, cfg.ProviderID, cfg.Model, err)
	}
	if strings.TrimSpace(profile.APIKey) == "" {
		return liveConfig{}, fmt.Errorf("%s: provider %s:%s has no usable credentials", path, cfg.ProviderID, cfg.Model)
	}
	return liveConfig{
		name: cfg.ProviderID + ":" + cfg.Model,
		path: path,
		cfg:  cfg,
	}, nil
}

func writeLiveProviderConfig(root, source, output, modelRef string) error {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return fmt.Errorf("select live provider config: uv is required: %w", err)
	}
	args := []string{
		"run", "--quiet", "--project", root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--source", source,
		"--output", output,
	}
	if modelRef != "" {
		args = append(args, "--ref", modelRef)
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = root
	combined, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(combined))
		if detail == "" {
			detail = "no diagnostic output"
		}
		return fmt.Errorf("select live provider config from %s: %w: %s", source, err, detail)
	}
	return nil
}

// runLiveTurn drives one real LLM turn with the supplied prompt against the
// shared builtin tool registry, in a fresh tempdir thread.
func runLiveTurn(t *testing.T, cfg config.Config, userPrompt string) string {
	t.Helper()
	profile, err := cfg.ProviderProfile()
	if err != nil {
		t.Fatalf("provider profile: %v", err)
	}
	provider, err := llm.NewProvider(profile)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{Shell: cfgShellProfile(cfg)})

	bus := events.NewBus()
	threadState, err := thread.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer threadState.Close()
	threadState.SubscribeBus(bus)

	pb := e2ePromptBuilder(
		t,
		"",
		[]string{t.TempDir()}, // empty
		"",
		promptcontext.ShellProfileFromConfig(cfg.Shell),
		func() time.Time { return time.Now().UTC() },
		threadState,
	)
	eng := &runtime.Engine{
		Provider: provider, Tools: reg, Bus: bus, Thread: threadState, Prompt: pb,
	}

	bus.Subscribe("*", func(e events.Event) {
		t.Logf("[event] %s payload=%v", e.Type, e.Payload)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	out, err := eng.Turn(ctx, userPrompt)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	t.Logf("model said: %s", out)
	return out
}

func cfgShellProfile(cfg config.Config) tools.ShellProfile {
	return tools.ShellProfile{
		Profile:       cfg.Shell.Profile,
		Family:        cfg.Shell.Family,
		Binary:        cfg.Shell.Binary,
		Args:          append([]string(nil), cfg.Shell.Args...),
		PathStyle:     cfg.Shell.PathStyle,
		HostPathStyle: cfg.Shell.HostPathStyle,
	}
}

func liveWorkspaceTempDir(t *testing.T, cfg config.Config, pattern string) string {
	t.Helper()
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = repoRoot(t)
	}
	base := filepath.Join(workDir, ".juex")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

func TestLiveConfigs_PlainCompletion(t *testing.T) {
	for _, lc := range loadLiveConfigs(t) {
		t.Run(lc.name, func(t *testing.T) {
			t.Logf("using config %s", lc.path)
			out := runLiveTurn(t, lc.cfg, "Reply with exactly one word: PONG")
			if !strings.Contains(strings.ToUpper(out), "PONG") {
				t.Fatalf("expected PONG in response, got %q", out)
			}
		})
	}
}

func TestLiveConfigs_ToolUse(t *testing.T) {
	for _, lc := range loadLiveConfigs(t) {
		t.Run(lc.name, func(t *testing.T) {
			t.Logf("using config %s", lc.path)
			dir := liveWorkspaceTempDir(t, lc.cfg, "live-tool-")
			target := filepath.Join(dir, "secret.txt")
			if err := os.WriteFile(target, []byte("the magic phrase is JUEX_LIVE_42"), 0o644); err != nil {
				t.Fatal(err)
			}
			prompt := "Inside the current Juex workdir, there is a file at " + target +
				". Use the `read` tool to read it, then reply containing the magic phrase verbatim."
			out := runLiveTurn(t, lc.cfg, prompt)
			if !strings.Contains(out, "JUEX_LIVE_42") {
				t.Fatalf("model did not surface phrase from file; got %q", out)
			}
		})
	}
}

func TestLiveConfigs_ExternalizedToolResultRetrieval(t *testing.T) {
	const toolName = "emit_large_result"
	for _, lc := range loadLiveConfigs(t) {
		t.Run(lc.name, func(t *testing.T) {
			t.Logf("using config %s", lc.path)
			profile, err := lc.cfg.ProviderProfile()
			if err != nil {
				t.Fatalf("provider profile: %v", err)
			}
			provider, err := llm.NewProvider(profile)
			if err != nil {
				t.Fatalf("provider: %v", err)
			}

			root := t.TempDir()
			workDir := filepath.Join(root, "work")
			agentStateDir := filepath.Join(root, "agent")
			for _, dir := range []string{workDir, agentStateDir} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			threadState, err := thread.New(filepath.Join(agentStateDir, "threads"))
			if err != nil {
				t.Fatal(err)
			}
			defer threadState.Close()
			bus := events.NewBus()
			threadState.SubscribeBus(bus)

			registry := tools.NewRegistry()
			tools.RegisterBuiltins(registry, tools.BuiltinOptions{
				WorkDir: workDir, AgentStateDir: agentStateDir, MediaDir: threadState.SpoolDir(),
				Shell: cfgShellProfile(lc.cfg),
			})
			const secret = "JUEX_OMITTED_42"
			original := strings.Join([]string{
				`RECOVERY INSTRUCTION: the only target line starts with "SECRET_TOKEN:" and is in the omitted middle. Use exec_command to run rg '^SECRET_TOKEN:' against the path in the truncation wrapper.`,
				strings.Repeat("head filler without the target token\n", 4_000),
				"SECRET_TOKEN: " + secret,
				strings.Repeat("tail filler without the target token\n", 4_000),
				"END OF LARGE RESULT",
			}, "\n")
			registry.MustRegister(tools.Tool{
				Name:        toolName,
				Description: "Return a large diagnostic document whose secret must be recovered from the persisted complete result.",
				Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
				Handler: func(context.Context, map[string]any) (string, error) {
					return original, nil
				},
			})

			engine := &runtime.Engine{
				Provider: provider,
				Tools:    registry,
				Bus:      bus,
				Thread:   threadState,
				Prompt: e2ePromptBuilder(t, "", []string{workDir}, workDir, promptcontext.ShellProfileFromConfig(lc.cfg.Shell), func() time.Time {
					return time.Now().UTC()
				}, threadState),
				WorkDir:         workDir,
				MediaDir:        threadState.SpoolDir(),
				ContextWindow:   lc.cfg.ContextWindow,
				MaxOutputTokens: lc.cfg.MaxOutputTokens,
				Compaction:      lc.cfg.Compaction,
				ToolOutput:      lc.cfg.ToolOutput,
			}
			bus.Subscribe("*", func(event events.Event) {
				if strings.HasSuffix(event.Type, "delta") {
					return
				}
				t.Logf("[event] %s", event.Type)
			})

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
			defer cancel()
			out, err := engine.Turn(ctx, "Call emit_large_result exactly once. Do not guess the secret. After its truncated Tool Result gives you a path, call exec_command as instructed in the Preview to inspect the persisted complete result, then reply with the exact SECRET_TOKEN value only.")
			if err != nil {
				t.Fatalf("Turn: %v", err)
			}
			if !strings.Contains(out, secret) {
				t.Fatalf("model did not recover omitted secret %q; got %q", secret, out)
			}

			var projected *llm.ContextArtifactProjection
			sawProjectedResult := false
			sawRetrievalCall := false
			for _, message := range threadState.History {
				for _, block := range message.Blocks {
					if block.Type == llm.BlockToolResult && block.Artifact != nil && block.Artifact.ToolName == toolName {
						projection := *block.Artifact
						projected = &projection
						sawProjectedResult = true
						if strings.Contains(block.Content, secret) || !strings.Contains(block.Content, "characters omitted") {
							t.Fatalf("provider-visible Tool Result did not hide the secret with an exact omission marker:\n%s", block.Content)
						}
						continue
					}
					if sawProjectedResult && block.Type == llm.BlockToolUse && (block.ToolName == "exec_command" || block.ToolName == "read" || block.ToolName == "grep") {
						sawRetrievalCall = true
					}
				}
			}
			if projected == nil || !sawRetrievalCall {
				t.Fatalf("history did not prove externalization followed by a retrieval Tool Call: %+v", threadState.History)
			}
			stored, err := os.ReadFile(filepath.Join(threadState.SpoolDir(), filepath.FromSlash(projected.StoredPath)))
			if err != nil {
				t.Fatalf("read persisted complete Tool Result: %v", err)
			}
			if string(stored) != original {
				t.Fatalf("persisted Tool Result changed: got %d bytes, want %d", len(stored), len(original))
			}
		})
	}
}

// TestLiveConfigs_MultiStep gives the model a workflow that requires writing,
// editing, and verifying — at least three tool rounds — to exercise the
// turn loop's iteration / parallelism paths against a real model.
func TestLiveConfigs_MultiStep(t *testing.T) {
	for _, lc := range loadLiveConfigs(t) {
		t.Run(lc.name, func(t *testing.T) {
			t.Logf("using config %s", lc.path)
			dir := liveWorkspaceTempDir(t, lc.cfg, "live-multistep-")
			target := filepath.Join(dir, "scratch.txt")

			prompt := "You will work in directory " + dir + ". " +
				"Step 1: use the `write` tool to create scratch.txt with content `start`. " +
				"Step 2: use the `edit` tool to replace `start` with `JUEX_LIVE_42`. " +
				"Step 3: use the `exec_command` tool to print " + target + " with the current shell syntax. " +
				"Step 4: reply with the final file contents only, on a single line."
			out := runLiveTurn(t, lc.cfg, prompt)
			if !strings.Contains(out, "JUEX_LIVE_42") {
				t.Fatalf("model did not produce expected output: %q", out)
			}
			// Filesystem side-effect must be observable.
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read scratch.txt: %v", err)
			}
			if !strings.Contains(string(data), "JUEX_LIVE_42") {
				t.Fatalf("scratch.txt content unexpected: %q", string(data))
			}
		})
	}
}
