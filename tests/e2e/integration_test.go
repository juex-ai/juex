//go:build integration

// Live integration smoke tests. Build-tag gated so they only run when
// explicitly opted in (no API key in CI). Reads .env.local.anthropic or
// .env.local.openai from the repository root.
//
//	go test -tags=integration ./internal/e2e/ -run Live
//	# or pick a provider:
//	go test -tags=integration ./internal/e2e/ -run LiveOpenAI
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
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

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

func loadEnv(t *testing.T, name string) config.Config {
	t.Helper()
	root := repoRoot(t)
	envPath := filepath.Join(root, name)
	if _, err := os.Stat(envPath); err != nil {
		t.Skipf("%s not present (%v); skipping live test", envPath, err)
	}
	// Clear OS env vars so the .env file wins.
	for _, k := range []string{"PROVIDER_API_TYPE", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL"} {
		t.Setenv(k, "")
	}
	cfg, err := config.LoadFromFile(envPath)
	if err != nil {
		t.Fatalf("load env: %v", err)
	}
	if cfg.APIKey == "" {
		t.Skipf("%s has no API key set", envPath)
	}
	return cfg
}

// runLiveTurn drives one real LLM turn with the supplied prompt against the
// shared builtin tool registry, in a fresh tempdir session.
func runLiveTurn(t *testing.T, cfg config.Config, userPrompt string) string {
	t.Helper()
	provider, err := cfg.NewProvider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}

	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, "")

	bus := events.NewBus()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.SubscribeBus(bus)

	pb := &prompt.Builder{
		AgentsMDDirs: []string{t.TempDir()}, // empty
		Now:          func() time.Time { return time.Now().UTC() },
	}
	eng := &runtime.Engine{
		Provider: provider, Tools: reg, Bus: bus, Session: sess, Prompt: pb,
		MaxIters: 10, MaxDur: 60 * time.Second,
	}

	bus.Subscribe("*", func(e events.Event) {
		t.Logf("[event] %s payload=%v", e.Type, e.Payload)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := eng.Turn(ctx, userPrompt)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	t.Logf("model said: %s", out)
	return out
}

func TestLiveOpenAI_PlainCompletion(t *testing.T) {
	cfg := loadEnv(t, ".env.local.openai")
	out := runLiveTurn(t, cfg, "Reply with exactly one word: PONG")
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Fatalf("expected PONG in response, got %q", out)
	}
}

func TestLiveOpenAI_ToolUse(t *testing.T) {
	cfg := loadEnv(t, ".env.local.openai")
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(target, []byte("the magic phrase is JUEX_LIVE_42"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := "There is a file at " + target +
		". Use the `read` tool to read it, then reply containing the magic phrase verbatim."
	out := runLiveTurn(t, cfg, prompt)
	if !strings.Contains(out, "JUEX_LIVE_42") {
		t.Fatalf("model did not surface phrase from file; got %q", out)
	}
}

func TestLiveAnthropic_PlainCompletion(t *testing.T) {
	cfg := loadEnv(t, ".env.local.anthropic")
	out := runLiveTurn(t, cfg, "Reply with exactly one word: PONG")
	if !strings.Contains(strings.ToUpper(out), "PONG") {
		t.Fatalf("expected PONG in response, got %q", out)
	}
}

func TestLiveAnthropic_ToolUse(t *testing.T) {
	cfg := loadEnv(t, ".env.local.anthropic")
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(target, []byte("the magic phrase is JUEX_LIVE_42"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := "There is a file at " + target +
		". Use the `read` tool to read it, then reply containing the magic phrase verbatim."
	out := runLiveTurn(t, cfg, prompt)
	if !strings.Contains(out, "JUEX_LIVE_42") {
		t.Fatalf("model did not surface phrase from file; got %q", out)
	}
}

// TestLiveOpenAI_MultiStep gives the model a workflow that requires writing,
// editing, and verifying — at least three tool rounds — to exercise the
// turn loop's iteration / parallelism paths against a real model.
func TestLiveOpenAI_MultiStep(t *testing.T) {
	cfg := loadEnv(t, ".env.local.openai")
	dir := t.TempDir()
	target := filepath.Join(dir, "scratch.txt")

	prompt := "You will work in directory " + dir + ". " +
		"Step 1: use the `write` tool to create scratch.txt with content `start`. " +
		"Step 2: use the `edit` tool to replace `start` with `JUEX_LIVE_42`. " +
		"Step 3: use the `bash` tool to run `cat " + target + "`. " +
		"Step 4: reply with the final file contents only, on a single line."
	out := runLiveTurn(t, cfg, prompt)
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
}
