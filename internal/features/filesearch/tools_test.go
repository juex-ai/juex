package filesearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestBuiltins_Grep(t *testing.T) {
	r := toolcore.NewRegistry()
	filesearchToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\nalphabet"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("gamma"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "alpha", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 hits, got %d in:\n%s", len(lines), out)
	}
}

func TestBuiltins_GrepRespectsSandboxBlockedPaths(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "public.txt"), []byte("needle public"), 0o644); err != nil {
		t.Fatal(err)
	}
	blockedDir := filepath.Join(workDir, "private")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedDir, "secret.txt"), []byte("needle secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	filesearchToolstestRegisterSandboxedTestBuiltins(r, workDir, []string{"private"})

	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "needle", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "public.txt") || strings.Contains(out, "secret") {
		t.Fatalf("grep output = %q, want public hit without blocked content", out)
	}
	if _, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "needle", "path": "private"}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("direct blocked grep err = %v, want blocked path", err)
	}
}

func TestBuiltins_GrepNoMatches(t *testing.T) {
	r := toolcore.NewRegistry()
	filesearchToolstestRegisterTestBuiltins(r, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "zzz", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("want no matches, got %q", out)
	}
}

func TestBuiltins_GrepRegex(t *testing.T) {
	r := toolcore.NewRegistry()
	filesearchToolstestRegisterTestBuiltins(r, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nthree\n42abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": `^\d+`, "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "42abc") {
		t.Fatalf("regex match failed: %q", out)
	}
}

func TestBuiltins_GrepDefaultsToWorkDir(t *testing.T) {
	r := toolcore.NewRegistry()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	filesearchToolstestRegisterTestBuiltins(r, dir)
	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("grep result = %q", out)
	}
}

type filesearchToolstestFakeSandboxRunner struct {
	calls    int
	specs    []sandbox.ExecSpec
	requests []sandbox.Request
	err      error
	prepare  func(context.Context, sandbox.Request) (sandbox.ExecSpec, error)
}

func filesearchToolstestRegisterSandboxedTestBuiltins(r *toolcore.Registry, workDir string, blockedPaths []string) {
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = blockedPaths
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		Shell:         command.DefaultShellProfile(),
		Sandbox:       policy,
		SandboxRunner: &filesearchToolstestFakeSandboxRunner{},
	})
}

func filesearchToolstestRegisterTestBuiltins(r *toolcore.Registry, workDir string) {
	registerTestTools(r, testToolOptions{WorkDir: workDir, MediaDir: filesearchToolstestTestMediaDir(workDir), Shell: command.DefaultShellProfile()})
}

func filesearchToolstestTestMediaDir(workDir string) string { return filepath.Join(workDir, "media") }

func (r *filesearchToolstestFakeSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
	r.calls++
	r.specs = append(r.specs, req.Spec)
	r.requests = append(r.requests, req)
	if r.prepare != nil {
		return r.prepare(ctx, req)
	}
	if r.err != nil {
		return sandbox.ExecSpec{}, r.err
	}
	return req.Spec, nil
}
