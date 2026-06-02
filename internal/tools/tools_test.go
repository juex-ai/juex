package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestRegistry_NormalizesNullSchemaEntries(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Tool{
		Name: "x",
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": nil,
			"default":              nil,
			"properties": map[string]any{
				"query":    nil,
				"mode":     map[string]any{"enum": []any{"all", nil}},
				"bad_enum": map[string]any{"enum": nil},
			},
			"patternProperties": map[string]any{"^x-": nil},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := r.Get("x")
	if !ok {
		t.Fatal("expected registered tool")
	}
	if _, ok := tool.Schema["additionalProperties"]; ok {
		t.Fatalf("additionalProperties null should be removed: %+v", tool.Schema)
	}
	if _, ok := tool.Schema["default"]; ok {
		t.Fatalf("default null should be removed: %+v", tool.Schema)
	}
	props, ok := tool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", tool.Schema["properties"])
	}
	if query, ok := props["query"].(map[string]any); !ok || len(query) != 0 {
		t.Fatalf("null property schema should become empty object: %+v", props["query"])
	}
	badEnum, _ := props["bad_enum"].(map[string]any)
	if _, ok := badEnum["enum"]; ok {
		t.Fatalf("enum:null should be removed: %+v", badEnum)
	}
	mode, _ := props["mode"].(map[string]any)
	enum, _ := mode["enum"].([]any)
	if len(enum) != 2 || enum[1] != nil {
		t.Fatalf("enum null values should be preserved: %+v", enum)
	}
	patternProps, ok := tool.Schema["patternProperties"].(map[string]any)
	if !ok {
		t.Fatalf("patternProperties = %+v", tool.Schema["patternProperties"])
	}
	if pattern, ok := patternProps["^x-"].(map[string]any); !ok || len(pattern) != 0 {
		t.Fatalf("null pattern property schema should become empty object: %+v", patternProps["^x-"])
	}
}

func TestRegistry_SpecsExposeReservedTimeout(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name: "slow",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []string{"value"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	specs := r.Specs()
	if len(specs) != 1 {
		t.Fatalf("spec count = %d", len(specs))
	}
	props, ok := specs[0].Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", specs[0].Schema["properties"])
	}
	timeout, ok := props["timeout"].(map[string]any)
	if !ok {
		t.Fatalf("timeout property missing from schema: %+v", props)
	}
	if timeout["type"] != "integer" {
		t.Fatalf("timeout schema = %+v, want integer", timeout)
	}
	if _, required := timeout["required"]; required {
		t.Fatalf("timeout property should not be required: %+v", timeout)
	}
}

func TestRegistry_CallWithInfoAppliesTimeoutAndStripsReservedInput(t *testing.T) {
	r := NewRegistry()
	seen := make(chan map[string]any, 1)
	if err := r.Register(Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			seen <- in
			<-ctx.Done()
			return "", ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	out, info, err := r.CallWithInfo(context.Background(), "slow", map[string]any{"timeout": 1, "value": "x"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if !info.TimedOut || info.TimeoutSeconds != 1 {
		t.Fatalf("info = %+v, want timed out after 1s", info)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("err = %v, want timed out after 1s", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
	input := <-seen
	if _, ok := input["timeout"]; ok {
		t.Fatalf("reserved timeout leaked to handler: %+v", input)
	}
	if input["value"] != "x" {
		t.Fatalf("handler input = %+v", input)
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

func TestBuiltins_EditReplaceAll(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("alpha beta alpha alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := r.Call(context.Background(), "edit", map[string]any{
		"path":        path,
		"old":         "alpha",
		"new":         "omega",
		"replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 replacements") {
		t.Fatalf("edit output: %s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "omega beta omega omega" {
		t.Fatalf("after edit: %q", string(data))
	}
}

func TestBuiltins_EditExpectedReplacementsMismatchPreservesFile(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	original := "one two one"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Call(context.Background(), "edit", map[string]any{
		"path":                  path,
		"old":                   "one",
		"new":                   "three",
		"replace_all":           true,
		"expected_replacements": 3,
	})
	if err == nil {
		t.Fatal("expected replacement count error")
	}
	if !strings.Contains(err.Error(), "expected 3 replacements, found 2") {
		t.Fatalf("unexpected error: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Fatalf("file changed after failed edit: %q", string(data))
	}
}

func TestBuiltins_EditExpectedReplacementsNullIsIgnored(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{
		"path":                  path,
		"old":                   "world",
		"new":                   "Juex",
		"expected_replacements": nil,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello Juex" {
		t.Fatalf("after edit: %q", string(data))
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
	out, info, err := r.CallWithInfo(context.Background(), "bash", map[string]any{
		"cmd":     "printf 'before timeout stdout\\n'; printf 'before timeout stderr\\n' >&2; sleep 5",
		"timeout": 1,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(out, "before timeout stdout") || !strings.Contains(out, "before timeout stderr") {
		t.Fatalf("timeout output = %q, want captured stdout/stderr", out)
	}
	if !info.TimedOut || info.TimeoutSeconds != 1 {
		t.Fatalf("info = %+v, want timed out after 1s", info)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("expected timeout error, got %v", err)
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
