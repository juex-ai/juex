//go:build integration

// Live integration smoke tests. Build-tag gated so they only run when
// explicitly opted in (no API key in normal CI). Reads provider configs from
// .juex/*.yaml in the repository root.
//
//	go test -tags=integration ./tests/e2e/... -run Live
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

var liveConfigEnvKeys = []string{
	"PROVIDER_API_ID",
	"PROVIDER_API_PROTOCOL",
	"PROVIDER_API_BASE",
	"PROVIDER_API_KEY",
	"PROVIDER_API_MODEL",
	"PROVIDER_THINKING_EFFORT",
	"PROVIDER_CONTEXT_WINDOW",
}

var defaultLiveConfigNames = []string{
	"qwen.juex.yaml",
	"minimax.juex.yaml",
}

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
	var matches []string
	for _, name := range defaultLiveConfigNames {
		path := filepath.Join(root, ".juex", name)
		_, err := os.Stat(path)
		if err == nil {
			matches = append(matches, path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("error checking for live config %s: %v", path, err)
		}
	}
	if len(matches) == 0 {
		t.Skip("none of .juex/qwen.juex.yaml or .juex/minimax.juex.yaml are present; skipping live tests")
	}
	// Clear OS env vars so the explicit .juex/*.yaml file wins.
	for _, k := range liveConfigEnvKeys {
		t.Setenv(k, "")
	}

	var out []liveConfig
	for _, path := range matches {
		cfg, err := config.LoadFromFileForWorkDir(path, root)
		if err != nil {
			t.Fatalf("load live config %s: %v", path, err)
		}
		if cfg.APIKey == "" {
			t.Logf("%s has no API key set; skipping it", path)
			continue
		}
		if (cfg.ProviderID == "" && cfg.ProviderProtocol == "") || cfg.Model == "" {
			t.Logf("%s has incomplete provider config; skipping it", path)
			continue
		}
		out = append(out, liveConfig{
			name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			path: path,
			cfg:  cfg,
		})
	}
	if len(out) == 0 {
		t.Skip("no usable .juex/*.yaml live configs found; skipping live tests")
	}
	return out
}

// runLiveTurn drives one real LLM turn with the supplied prompt against the
// shared builtin tool registry, in a fresh tempdir session.
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
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.SubscribeBus(bus)

	pb := &prompt.Builder{
		AgentsMDDirs: []string{t.TempDir()}, // empty
		Shell:        prompt.ShellProfileFromConfig(cfg.Shell),
		Now:          func() time.Time { return time.Now().UTC() },
	}
	eng := &runtime.Engine{
		Provider: provider, Tools: reg, Bus: bus, Session: sess, Prompt: pb,
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
