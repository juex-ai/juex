package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// skipIfWindows guards bash-dependent tests on platforms without a default
// bash. CI runs windows-latest where /bin/bash is absent.
func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash tool requires bash; skipping on windows")
	}
}

var _ = filepath.Join

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	tool := Tool{Name: "x", Handler: func(ctx context.Context, in map[string]any) (string, error) { return "", nil }}
	if err := r.Register(tool); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(tool); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestBuiltins_ReadWriteEdit(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")

	out, err := r.Call(context.Background(), "write", map[string]any{"path": path, "content": "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("write output: %s", out)
	}

	out, err = r.Call(context.Background(), "read", map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("read output: %q", out)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": path, "old": "world", "new": "Juex"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello Juex" {
		t.Fatalf("after edit: %q", string(data))
	}
}

func TestBuiltins_EditAmbiguous(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": path, "old": "a", "new": "b"}); err == nil {
		t.Fatal("expected ambiguity error")
	}
}

func TestBuiltins_Bash(t *testing.T) {
	skipIfWindows(t)
	r := NewRegistry()
	RegisterBuiltins(r, "")
	out, err := r.Call(context.Background(), "bash", map[string]any{"cmd": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("bash output: %q", out)
	}
}

func TestBuiltins_Grep(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")

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

func TestBuiltins_GrepNoMatches(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")
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

func TestBuiltins_BashTimeout(t *testing.T) {
	skipIfWindows(t)
	r := NewRegistry()
	RegisterBuiltins(r, "")
	out, err := r.Call(context.Background(), "bash", map[string]any{"cmd": "sleep 5", "timeout": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit error") {
		t.Fatalf("expected timeout exit error, got %q", out)
	}
}

func TestBuiltins_BashCwd(t *testing.T) {
	skipIfWindows(t)
	r := NewRegistry()
	RegisterBuiltins(r, "")
	dir := t.TempDir()
	out, err := r.Call(context.Background(), "bash", map[string]any{"cmd": "pwd", "cwd": dir})
	if err != nil {
		t.Fatal(err)
	}
	// On macOS /tmp is a symlink to /private/tmp so just check the basename.
	if !strings.Contains(out, filepath.Base(dir)) {
		t.Fatalf("expected pwd output to contain %s, got %q", dir, out)
	}
}

func TestBuiltins_ReadOffsetLimit(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{"path": path, "offset": 2, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if out != "b\nc" {
		t.Fatalf("got %q", out)
	}
}

func TestBuiltins_GrepRegex(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")
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

func TestBuiltins_EditMissingFile(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")
	_, err := r.Call(context.Background(), "edit", map[string]any{"path": "/tmp/__nope__.txt", "old": "x", "new": "y"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuiltins_BashDefaultsToWorkDir(t *testing.T) {
	skipIfWindows(t)
	r := NewRegistry()
	dir := t.TempDir()
	RegisterBuiltins(r, dir)
	out, err := r.Call(context.Background(), "bash", map[string]any{"cmd": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Base(dir)) {
		t.Fatalf("bash defaulted to %q, want under %s", out, dir)
	}
}

func TestBuiltins_GrepDefaultsToWorkDir(t *testing.T) {
	r := NewRegistry()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	RegisterBuiltins(r, dir)
	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("grep result = %q", out)
	}
}

func TestBuiltins_BashCwdOverridesWorkDir(t *testing.T) {
	skipIfWindows(t)
	// Explicit cwd in the call wins over the configured WorkDir.
	r := NewRegistry()
	work := t.TempDir()
	other := t.TempDir()
	RegisterBuiltins(r, work)
	out, err := r.Call(context.Background(), "bash", map[string]any{"cmd": "pwd", "cwd": other})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Base(other)) {
		t.Fatalf("expected cwd to win, got %q", out)
	}
}

func TestSpecs_OrderedAndComplete(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")
	specs := r.Specs()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	want := []string{"bash", "edit", "grep", "read", "write"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %v", want, names)
	}
}
