package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipIfWindows guards tests that intentionally exercise POSIX-only command
// syntax or process-group behavior.
func skipIfWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell syntax")
	}
}

func registerTestBuiltins(r *Registry, workDir string) {
	RegisterBuiltins(r, BuiltinOptions{WorkDir: workDir, Shell: DefaultShellProfile()})
}

func pwdCommand() string {
	if runtime.GOOS == "windows" {
		return "cd"
	}
	return "pwd"
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

func TestBuiltins_ShellUsesConfiguredProfileAndCwd(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	callDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "shell.json")
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MARKER", marker)

	RegisterBuiltins(r, BuiltinOptions{
		WorkDir: workDir,
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, err := r.Call(context.Background(), "shell", map[string]any{"cmd": "echo hi", "cwd": callDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fake shell ok") {
		t.Fatalf("out = %q, want fake shell output", out)
	}

	var payload struct {
		Cwd  string   `json:"cwd"`
		Args []string `json:"args"`
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Cwd != callDir {
		t.Fatalf("cwd = %q, want %q", payload.Cwd, callDir)
	}
	if len(payload.Args) == 0 || payload.Args[len(payload.Args)-1] != "echo hi" {
		t.Fatalf("args = %#v, want command appended as final arg", payload.Args)
	}
	if _, ok := r.Get("bash"); ok {
		t.Fatal("bash tool should not be registered")
	}
}

func TestBuiltins_ShellRelativeCwdResolvesFromWorkDir(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	relativeDir := "nested"
	wantDir := filepath.Join(workDir, relativeDir)
	if err := os.MkdirAll(wantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "shell.json")
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MARKER", marker)

	RegisterBuiltins(r, BuiltinOptions{
		WorkDir: workDir,
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	if _, err := r.Call(context.Background(), "shell", map[string]any{"cmd": "echo hi", "cwd": relativeDir}); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Cwd string `json:"cwd"`
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Cwd != wantDir {
		t.Fatalf("cwd = %q, want %q", payload.Cwd, wantDir)
	}
}

func TestShellHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_FAKE_SHELL") != "1" {
		return
	}
	payload := map[string]any{
		"args": os.Args,
	}
	if cwd, err := os.Getwd(); err == nil {
		payload["cwd"] = cwd
	}
	if marker := os.Getenv("JUEX_FAKE_SHELL_MARKER"); marker != "" {
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(marker, data, 0o644)
	}
	fmt.Fprintln(os.Stdout, "fake shell ok")
	os.Exit(0)
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

func TestRegistry_CallWithInfoParsesRawArgumentsBeforeDispatch(t *testing.T) {
	r := NewRegistry()
	seen := make(chan map[string]any, 1)
	if err := r.Register(Tool{
		Name: "echo",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout": map[string]any{"type": "integer"},
			},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			seen <- in
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "echo", map[string]any{
		"_raw_arguments": `{"value":"x","timeout":2}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want ok", out)
	}
	if info.TimeoutSeconds != 2 {
		t.Fatalf("timeout = %d, want 2", info.TimeoutSeconds)
	}
	input := <-seen
	timeout, ok := toInt(input["timeout"])
	if input["value"] != "x" || !ok || timeout != 2 {
		t.Fatalf("handler input = %+v, want decoded raw arguments", input)
	}
	if _, ok := input["_raw_arguments"]; ok {
		t.Fatalf("raw arguments leaked to handler: %+v", input)
	}
}

func TestBuiltins_ReadWriteEdit(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")

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

func TestBuiltins_FileToolsResolveRelativePathsFromWorkDir(t *testing.T) {
	processDir := t.TempDir()
	t.Chdir(processDir)
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(workDir, "music", "README.md")
	if err := os.WriteFile(readmePath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	out, err := r.Call(context.Background(), "read", map[string]any{"path": "music/README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("read output = %q, want workdir file contents", out)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{
		"path": "music/README.md",
		"old":  "world",
		"new":  "Juex",
	}); err != nil {
		t.Fatal(err)
	}
	edited, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "hello Juex" {
		t.Fatalf("edited file = %q, want workdir file updated", edited)
	}

	if _, err := r.Call(context.Background(), "write", map[string]any{
		"path":    "music/transposed.json",
		"content": `{"ok":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "music", "transposed.json")); err != nil {
		t.Fatalf("write did not create file under workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(processDir, "music", "transposed.json")); !os.IsNotExist(err) {
		t.Fatalf("write used process cwd; stat err = %v", err)
	}
}

func TestBuiltins_RelativeWorkDirIsCapturedAsAbsolute(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "workspace")
	if err := os.MkdirAll(filepath.Join(workDir, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "music", "README.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)

	r := NewRegistry()
	registerTestBuiltins(r, "workspace")
	t.Chdir(t.TempDir())

	out, err := r.Call(context.Background(), "read", map[string]any{"path": "music/README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "rules" {
		t.Fatalf("read output = %q, want file from original workdir", out)
	}
}

func TestBuiltins_ShellAcceptsRawArgumentsFallback(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")

	for name, input := range map[string]map[string]any{
		"object": {
			"_raw_arguments": `{"cmd":"echo raw-ok"}`,
		},
		"double_encoded": {
			"_raw_arguments": `"{\"cmd\":\"echo raw-ok\"}"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := r.Call(context.Background(), "shell", input)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(out) != "raw-ok" {
				t.Fatalf("out = %q, want raw-ok", out)
			}
		})
	}
}

func TestBuiltins_EditAmbiguous(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")

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
	registerTestBuiltins(r, "")

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
	registerTestBuiltins(r, "")

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
	registerTestBuiltins(r, "")

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

func TestBuiltins_Shell(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")
	out, err := r.Call(context.Background(), "shell", map[string]any{"cmd": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("shell output: %q", out)
	}
}

func TestBuiltins_Grep(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")

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
	registerTestBuiltins(r, "")
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

func TestBuiltins_ShellTimeout(t *testing.T) {
	skipIfWindows(t)
	r := NewRegistry()
	registerTestBuiltins(r, "")
	out, info, err := r.CallWithInfo(context.Background(), "shell", map[string]any{
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

func TestBuiltins_ShellTimeoutKillsChildProcessGroup(t *testing.T) {
	skipIfWindows(t)
	r := NewRegistry()
	registerTestBuiltins(r, "")

	start := time.Now()
	out, info, err := r.CallWithInfo(context.Background(), "shell", map[string]any{
		"cmd":     "printf 'child still owns pipe\\n'; sleep 5 & wait",
		"timeout": 1,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(out, "child still owns pipe") {
		t.Fatalf("timeout output = %q, want captured stdout", out)
	}
	if !info.TimedOut || info.TimeoutSeconds != 1 {
		t.Fatalf("info = %+v, want timed out after 1s", info)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("expected normalized timeout error, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout waited for child process to exit: %s", elapsed)
	}
}

func TestBuiltins_ShellCwd(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")
	dir := t.TempDir()
	out, err := r.Call(context.Background(), "shell", map[string]any{"cmd": pwdCommand(), "cwd": dir})
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
	registerTestBuiltins(r, "")
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
	registerTestBuiltins(r, "")
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
	registerTestBuiltins(r, "")
	_, err := r.Call(context.Background(), "edit", map[string]any{"path": "/tmp/__nope__.txt", "old": "x", "new": "y"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuiltins_ShellDefaultsToWorkDir(t *testing.T) {
	r := NewRegistry()
	dir := t.TempDir()
	registerTestBuiltins(r, dir)
	out, err := r.Call(context.Background(), "shell", map[string]any{"cmd": pwdCommand()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Base(dir)) {
		t.Fatalf("shell defaulted to %q, want under %s", out, dir)
	}
}

func TestBuiltins_GrepDefaultsToWorkDir(t *testing.T) {
	r := NewRegistry()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	registerTestBuiltins(r, dir)
	out, err := r.Call(context.Background(), "grep", map[string]any{"pattern": "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("grep result = %q", out)
	}
}

func TestBuiltins_ShellCwdOverridesWorkDir(t *testing.T) {
	// Explicit cwd in the call wins over the configured WorkDir.
	r := NewRegistry()
	work := t.TempDir()
	other := t.TempDir()
	registerTestBuiltins(r, work)
	out, err := r.Call(context.Background(), "shell", map[string]any{"cmd": pwdCommand(), "cwd": other})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Base(other)) {
		t.Fatalf("expected cwd to win, got %q", out)
	}
}

func TestSpecs_OrderedAndComplete(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")
	specs := r.Specs()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	want := []string{"edit", "grep", "read", "shell", "write"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %v", want, names)
	}
}
