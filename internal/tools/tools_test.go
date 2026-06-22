package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

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

func TestBuiltins_ExecCommandUsesConfiguredProfileAndWorkdir(t *testing.T) {
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

	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": "echo hi", "workdir": callDir})
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

func TestBuiltins_ExecCommandRelativeWorkdirResolvesFromWorkDir(t *testing.T) {
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

	if _, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": "echo hi", "workdir": relativeDir}); err != nil {
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
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "timeout" {
		fmt.Fprintln(os.Stdout, "before timeout stdout")
		fmt.Fprintln(os.Stderr, "before timeout stderr")
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "delayed" {
		fmt.Fprintln(os.Stdout, "first chunk")
		time.Sleep(500 * time.Millisecond)
		fmt.Fprintln(os.Stdout, "second chunk")
		os.Exit(0)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "stdin" {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprintf(os.Stdout, "got:%s", line)
		os.Exit(0)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "confirm" {
		fmt.Fprint(os.Stdout, "Install package? [yes/no] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) == "yes" {
			fmt.Fprintln(os.Stdout, "accepted")
			fmt.Fprintln(os.Stdout, "install complete")
			os.Exit(0)
		}
		fmt.Fprintln(os.Stdout, "declined")
		os.Exit(1)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "fail" {
		fmt.Fprintln(os.Stdout, "before failure stdout")
		fmt.Fprintln(os.Stderr, "before failure stderr")
		os.Exit(7)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "tty" {
		fmt.Fprintf(os.Stdout, "stdin_tty:%t stdout_tty:%t stderr_tty:%t\n", isCharDevice(os.Stdin), isCharDevice(os.Stdout), isCharDevice(os.Stderr))
		fmt.Fprint(os.Stdout, "enter value: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprintf(os.Stdout, "tty got:%s", line)
		os.Exit(0)
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

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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

func TestRegistry_SpecsPreserveToolSchema(t *testing.T) {
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
	if _, ok := props["timeout"]; ok {
		t.Fatalf("runtime timeout should not be injected into model schema: %+v", props)
	}
}

func TestRegistry_CallWithInfoAppliesConfiguredTimeout(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 1})
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
	out, info, err := r.CallWithInfo(context.Background(), "slow", map[string]any{"timeout": 9, "value": "x"})
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
	if input["timeout"] != 9 {
		t.Fatalf("model input timeout should not be interpreted as runtime policy: %+v", input)
	}
	if input["value"] != "x" {
		t.Fatalf("handler input = %+v", input)
	}
}

func TestRegistry_CallWithInfoUsesToolTimeoutOverride(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 5})
	if err := r.Register(Tool{
		Name:           "quick",
		Schema:         map[string]any{"type": "object"},
		TimeoutSeconds: 2,
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "quick", map[string]any{"timeout": 9})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want ok", out)
	}
	if info.TimeoutSeconds != 2 {
		t.Fatalf("timeout = %d, want tool override 2", info.TimeoutSeconds)
	}
}

func TestRegisterBuiltinsExecCommandInheritsRegistryTimeout(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 2})
	RegisterBuiltins(r, BuiltinOptions{Shell: DefaultShellProfile()})

	if got := r.TimeoutSecondsFor("exec_command"); got != 2 {
		t.Fatalf("exec_command timeout = %d, want registry default 2", got)
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
	if info.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Fatalf("timeout = %d, want default", info.TimeoutSeconds)
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

func TestBuiltins_EditMissingRequiredArgumentsReportsReceivedKeys(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("source_ai: Claude"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Call(context.Background(), "edit", map[string]any{
		"path":       path,
		"old_string": "source_ai: Claude",
		"new_string": "source_ai: Juex",
	})
	if err == nil {
		t.Fatal("expected missing required arguments error")
	}
	for _, want := range []string{
		"missing required argument(s): old, new",
		"expected keys: path, old, new",
		"received keys: new_string, old_string, path",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
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

func TestBuiltins_ExecCommandAcceptsRawArgumentsFallback(t *testing.T) {
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
			out, err := r.Call(context.Background(), "exec_command", input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Process exited with code 0") || !strings.Contains(out, "raw-ok") {
				t.Fatalf("out = %q, want successful raw-ok output", out)
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

func TestBuiltins_ExecCommand(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")
	out, info, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{"cmd": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	result := shellResultFromInfo(t, info)
	if result.Running || result.SessionID != 0 {
		t.Fatalf("shell result running/session = %+v, want completed without session id", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("shell result exit code = %+v, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Fatalf("shell structured output = %q, want hello", result.Output)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("shell output: %q", out)
	}
	if strings.Contains(out, "Process running with session ID") {
		t.Fatalf("quick exit output should not expose a session id: %q", out)
	}
	if !strings.Contains(out, "Original token count:") {
		t.Fatalf("exec output = %q, want original token count", out)
	}
}

func TestBuiltins_ExecCommandNonZeroExitReturnsError(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "fail")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, info, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd": "fail",
	})
	if err == nil {
		t.Fatalf("exec_command err = nil, output = %q", out)
	}
	result := shellResultFromInfo(t, info)
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("shell result exit code = %+v, want 7", result.ExitCode)
	}
	if code, ok := ExitCodeFromError(err); !ok || code != 7 {
		t.Fatalf("exec_command err = %v, want shell exit code 7", err)
	}
	if !strings.Contains(out, "Process exited with code 7") {
		t.Fatalf("exec output = %q, want exit code", out)
	}
	if !strings.Contains(out, "before failure stdout") || !strings.Contains(out, "before failure stderr") {
		t.Fatalf("exec output = %q, want captured stdout/stderr", out)
	}
}

func TestBuiltins_ExecCommandYieldReturnsSessionAndPollsLaterOutput(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, info, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "delayed",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	initialResult := shellResultFromInfo(t, info)
	if !initialResult.Running || initialResult.SessionID <= 0 {
		t.Fatalf("initial shell result = %+v, want running session", initialResult)
	}
	if initialResult.ChunkID <= 0 {
		t.Fatalf("initial shell result chunk id = %+v, want positive", initialResult)
	}
	sessionID := initialResult.SessionID
	if !strings.Contains(out, "Process running with session ID") {
		t.Fatalf("initial output = %q, want running status", out)
	}
	if !strings.Contains(out, "first chunk") {
		t.Fatalf("initial output = %q, want first chunk", out)
	}

	out, info, err = r.CallWithInfo(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"yield_time_ms": 800,
	})
	if err != nil {
		t.Fatal(err)
	}
	continuedResult := shellResultFromInfo(t, info)
	if continuedResult.Running || continuedResult.SessionID != 0 {
		t.Fatalf("continued shell result = %+v, want completed", continuedResult)
	}
	if continuedResult.ExitCode == nil || *continuedResult.ExitCode != 0 {
		t.Fatalf("continued exit code = %+v, want 0", continuedResult.ExitCode)
	}
	if !strings.Contains(out, "Process exited with code 0") {
		t.Fatalf("poll output = %q, want exited status", out)
	}
	if strings.Contains(out, "Process running with session ID") {
		t.Fatalf("exited poll output should not expose a session id: %q", out)
	}
	if !strings.Contains(out, "second chunk") {
		t.Fatalf("poll output = %q, want second chunk", out)
	}
}

type timedOutStructuredTestResult struct{}

func (timedOutStructuredTestResult) ToolCallTimedOut() bool {
	return true
}

func TestRegistryCallWithInfoKeepsStructuredResult(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name:   "structured",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, input map[string]any) (Result, error) {
			return Result{
				Text:       "ok",
				Structured: map[string]any{"answer": 42},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "structured", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want ok", out)
	}
	structured, ok := info.StructuredResult.(map[string]any)
	if !ok || structured["answer"] != 42 {
		t.Fatalf("structured result = %#v", info.StructuredResult)
	}
}

func TestRegistryCallWithInfoHonorsStructuredTimeout(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 1})
	if err := r.Register(Tool{
		Name:   "structured_timeout",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, input map[string]any) (Result, error) {
			return Result{
				Text:       "partial output",
				Structured: timedOutStructuredTestResult{},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "structured_timeout", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "partial output" {
		t.Fatalf("out = %q, want partial output", out)
	}
	if !info.TimedOut || info.TimeoutSeconds != 1 {
		t.Fatalf("info = %+v, want structured timeout after 1s", info)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("err = %v, want timed out after 1s", err)
	}
}

func TestBuiltins_ExecCommandTTYWritesStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows tty coverage runs through ConPTY-specific tests")
	}
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "stdin")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "stdin",
		"tty":           true,
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionIDFromOutput(t, out)

	out, err = r.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"chars":         "hello\n",
		"yield_time_ms": 1500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "got:hello") {
		t.Fatalf("stdin output = %q, want echoed input", out)
	}
	if !strings.Contains(out, "Process exited with code 0") {
		t.Fatalf("stdin output = %q, want exited status", out)
	}
}

func TestBuiltins_WriteStdinCanAnswerInteractivePrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows tty coverage runs through ConPTY-specific tests")
	}
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "confirm")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "confirm",
		"tty":           true,
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionIDFromOutput(t, out)
	if !strings.Contains(out, "Process running with session ID") {
		t.Fatalf("initial output = %q, want running status", out)
	}
	if !strings.Contains(out, "Install package? [yes/no]") {
		t.Fatalf("initial output = %q, want interactive prompt", out)
	}

	out, err = r.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"chars":         "yes\n",
		"yield_time_ms": 1500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "accepted") || !strings.Contains(out, "install complete") {
		t.Fatalf("continued output = %q, want accepted install output", out)
	}
	if !strings.Contains(out, "Process exited with code 0") {
		t.Fatalf("continued output = %q, want successful exit", out)
	}
}

func TestBuiltins_WriteStdinRejectsNonTTYInput(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "delayed",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionIDFromOutput(t, out)

	out, err = r.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"chars":         "hello\n",
		"yield_time_ms": 500,
	})
	if err == nil {
		t.Fatalf("write_stdin output = %q, want non-tty stdin error", out)
	}
	if !strings.Contains(err.Error(), "stdin is closed for this session") {
		t.Fatalf("write_stdin err = %v, want stdin closed error", err)
	}
}

func TestBuiltins_ExecCommandTTYAllocatesTerminalAndAcceptsChars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows tty coverage runs through ConPTY-specific tests")
	}
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "tty")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "tty",
		"tty":           true,
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionIDFromOutput(t, out)
	for _, want := range []string{"stdin_tty:true", "stdout_tty:true", "stderr_tty:true", "enter value:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("initial tty output = %q, want %q", out, want)
		}
	}

	out, err = r.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"chars":         "green\n",
		"yield_time_ms": 1500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tty got:green") {
		t.Fatalf("continued tty output = %q, want tty response", out)
	}
	if !strings.Contains(out, "Process exited with code 0") {
		t.Fatalf("continued tty output = %q, want successful exit", out)
	}
}

func TestBuiltins_ExecCommandYieldEmitsOutputDeltas(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})
	deltas := make(chan OutputDelta, 10)
	ctx := WithToolCallEvents(context.Background(), ToolCallEvents{
		Name:      "exec_command",
		ToolUseID: "tool-1",
		Emit: func(delta OutputDelta) {
			deltas <- delta
		},
	})

	if _, err := r.Call(ctx, "exec_command", map[string]any{
		"cmd":           "delayed",
		"yield_time_ms": 250,
	}); err != nil {
		t.Fatal(err)
	}
	var delta OutputDelta
	select {
	case delta = <-deltas:
	case <-time.After(time.Second):
		t.Fatal("expected output delta")
	}
	if delta.Name != "exec_command" || delta.ToolUseID != "tool-1" || delta.SessionID == "" {
		t.Fatalf("delta metadata = %+v", delta)
	}
	if !strings.Contains(delta.Text, "first chunk") {
		t.Fatalf("delta text = %q, want first chunk", delta.Text)
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

func TestBuiltins_ExecCommandTimeout(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "timeout")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
		ToolTimeoutSeconds: 1,
	})
	out, info, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd": "ignored by fake shell",
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

func TestBuiltins_ExecCommandWorkdir(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")
	dir := t.TempDir()
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": pwdCommand(), "workdir": dir})
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

func TestBuiltins_ExecCommandDefaultsToWorkDir(t *testing.T) {
	r := NewRegistry()
	dir := t.TempDir()
	registerTestBuiltins(r, dir)
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": pwdCommand()})
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

func TestBuiltins_ExecCommandWorkdirOverridesWorkDir(t *testing.T) {
	// Explicit workdir in the call wins over the configured WorkDir.
	r := NewRegistry()
	work := t.TempDir()
	other := t.TempDir()
	registerTestBuiltins(r, work)
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": pwdCommand(), "workdir": other})
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
	want := []string{"edit", "exec_command", "grep", "read", "write", "write_stdin"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %v", want, names)
	}
	if _, ok := r.Get("shell"); ok {
		t.Fatal("legacy shell tool should not be registered")
	}
	if _, ok := r.Get("shell_input"); ok {
		t.Fatal("legacy shell_input tool should not be registered")
	}
}

func TestBuiltinSchemas_ExecCommandAndWriteStdinShape(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, "")
	specs := r.Specs()
	byName := map[string]map[string]any{}
	for _, spec := range specs {
		byName[spec.Name] = spec.Schema
	}

	execProps := schemaProperties(t, byName["exec_command"])
	for _, want := range []string{"cmd", "workdir", "tty", "yield_time_ms", "max_output_tokens"} {
		if _, ok := execProps[want]; !ok {
			t.Fatalf("exec_command schema missing %q: %+v", want, execProps)
		}
	}
	if _, ok := execProps["timeout"]; ok {
		t.Fatalf("exec_command schema should not expose runtime timeout: %+v", execProps)
	}
	if _, ok := execProps["cwd"]; ok {
		t.Fatalf("exec_command schema exposes legacy cwd: %+v", execProps)
	}

	stdinProps := schemaProperties(t, byName["write_stdin"])
	for _, want := range []string{"session_id", "chars", "yield_time_ms", "max_output_tokens"} {
		if _, ok := stdinProps[want]; !ok {
			t.Fatalf("write_stdin schema missing %q: %+v", want, stdinProps)
		}
	}
	if _, ok := stdinProps["timeout"]; ok {
		t.Fatalf("write_stdin schema should not expose runtime timeout: %+v", stdinProps)
	}
	if _, ok := stdinProps["stdin"]; ok {
		t.Fatalf("write_stdin schema exposes legacy stdin: %+v", stdinProps)
	}
	sessionIDSchema, _ := stdinProps["session_id"].(map[string]any)
	if sessionIDSchema["type"] != "integer" {
		t.Fatalf("write_stdin session_id schema = %+v, want integer", sessionIDSchema)
	}
}

func TestShellYieldClampMatchesExecSemantics(t *testing.T) {
	if got := clampShellYield(1*time.Millisecond, minShellYield, maxShellYield); got != minShellYield {
		t.Fatalf("exec yield clamp = %s, want %s", got, minShellYield)
	}
	if got := clampShellYield(1*time.Millisecond, defaultShellInputPollYield, maxShellInputPollYield); got != defaultShellInputPollYield {
		t.Fatalf("empty poll yield clamp = %s, want %s", got, defaultShellInputPollYield)
	}
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %+v", schema)
	}
	return props
}

func sessionIDFromOutput(t *testing.T, out string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Process running with session ID ") {
			sessionID, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Process running with session ID ")))
			if err != nil {
				t.Fatalf("invalid session id in output:\n%s", out)
			}
			return sessionID
		}
	}
	t.Fatalf("missing session id in output:\n%s", out)
	return 0
}

func shellResultFromInfo(t *testing.T, info CallInfo) ShellResult {
	t.Helper()
	result, ok := info.StructuredResult.(ShellResult)
	if !ok {
		t.Fatalf("structured result = %#v, want ShellResult", info.StructuredResult)
	}
	return result
}
