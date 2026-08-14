package e2e

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/sandbox"
)

func TestEndToEnd_OmittedSandboxConfigRestrictsExecCommandWrites(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("default sandbox is disabled on %s", runtime.GOOS)
	}
	root := t.TempDir()
	work := filepath.Join(root, "workspace")
	outsideDir := filepath.Join(root, "outside")
	for _, path := range []string{work, outsideDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cfg := loadOmittedSandboxE2EConfig(t, root, work)
	policy := cfg.SandboxPolicy()
	if !policy.Enabled || policy.FileSystem.OutsideWorkspace != sandbox.OutsideWorkspaceReadOnly {
		t.Fatalf("omitted sandbox policy = %+v, want enabled read_only default", policy)
	}
	a, err := app.New(app.Options{
		Config:     cfg,
		Provider:   &bareScriptProvider{},
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })

	workspaceFile := filepath.Join(work, "workspace.txt")
	stateFile := filepath.Join(cfg.AgentStateDir, "state.txt")
	moduleCache := filepath.Join(cfg.AgentStateDir, "tmp", "cache", "go-mod")
	moduleCacheFile := filepath.Join(moduleCache, "probe.txt")
	outsideFile := filepath.Join(outsideDir, "blocked.txt")
	command := strings.Join([]string{
		"printf workspace > " + shellQuoteE2E(workspaceFile),
		"printf state > " + shellQuoteE2E(stateFile),
		"test \"$GOMODCACHE\" = " + shellQuoteE2E(moduleCache),
		`mkdir -p "$GOMODCACHE"`,
		`printf module > "$GOMODCACHE/probe.txt"`,
		"if printf outside > " + shellQuoteE2E(outsideFile) + " 2>/dev/null; then outside=unexpected; else outside=blocked; fi",
		`printf 'outside=%s\n' "$outside"`,
	}, "\n")
	out, _, err := a.Engine.Tools.CallWithInfo(context.Background(), "exec_command", map[string]any{"cmd": command})
	if err != nil {
		t.Fatalf("sandboxed exec_command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "outside=blocked") {
		t.Fatalf("exec_command output = %q, want outside=blocked", out)
	}
	assertE2EFileContent(t, workspaceFile, "workspace")
	assertE2EFileContent(t, stateFile, "state")
	assertE2EFileContent(t, moduleCacheFile, "module")
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("outside file stat = %v, want missing", err)
	}
}

func TestEndToEnd_OmittedSandboxConfigFailsClosedWithoutBackend(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("default sandbox is disabled on %s", runtime.GOOS)
	}
	root := t.TempDir()
	work := filepath.Join(root, "workspace")
	emptyPath := filepath.Join(root, "empty-path")
	for _, path := range []string{work, emptyPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", emptyPath)
	cfg := loadOmittedSandboxE2EConfig(t, root, work)
	a, err := app.New(app.Options{
		Config:     cfg,
		Provider:   &bareScriptProvider{},
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })

	started := filepath.Join(work, "must-not-start.txt")
	out, _, err := a.Engine.Tools.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd": "printf started > " + shellQuoteE2E(started),
	})
	if err == nil {
		t.Fatalf("exec_command unexpectedly succeeded: %s", out)
	}
	backend := "bubblewrap"
	if runtime.GOOS == "darwin" {
		backend = "sandbox-exec"
	}
	if !strings.Contains(err.Error(), backend) {
		t.Fatalf("exec_command error = %v, want %s diagnostic", err, backend)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatalf("command started despite unavailable sandbox: %v", statErr)
	}
}

func loadOmittedSandboxE2EConfig(t *testing.T, root, work string) config.Config {
	t.Helper()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", filepath.Join(home, ".juex"))

	body := `model: test:model
providers:
  - id: test
    protocol: openai/chat
    base_url: https://example.invalid
    api_key: test-key
    models:
      - id: model
shell:
  profile: custom
  binary: /bin/sh
  family: posix
  args: ["-c"]
  path_style: posix
`
	configPath := filepath.Join(root, "juex.yaml")
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFromFileForWorkDir(configPath, work)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func shellQuoteE2E(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func assertE2EFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}
