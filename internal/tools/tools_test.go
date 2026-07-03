package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
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

func fakeShellProfile() ShellProfile {
	return ShellProfile{
		Profile:   "fake",
		Family:    "posix",
		Binary:    os.Args[0],
		Args:      []string{"-test.run=TestShellHelperProcess", "--"},
		PathStyle: "posix",
	}
}

type builtinProviderFunc func(BuiltinProviderContext) []Tool

func (f builtinProviderFunc) Tools(ctx BuiltinProviderContext) []Tool {
	return f(ctx)
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

func TestRegisterBuiltinsDefaultProviderToolSet(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())

	var names []string
	for _, tool := range r.List() {
		names = append(names, tool.Name)
	}
	want := []string{
		"apply_patch",
		"edit",
		"exec_command",
		"grep",
		"list_shell_sessions",
		"read",
		"write",
		"write_abort",
		"write_begin",
		"write_chunk",
		"write_commit",
		"write_stdin",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("builtin tools = %v, want %v", names, want)
	}
}

func TestRegisterBuiltinsCanUseCustomProviders(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	var gotCtx BuiltinProviderContext
	RegisterBuiltins(r, BuiltinOptions{
		WorkDir: workDir,
		Providers: []BuiltinProvider{builtinProviderFunc(func(ctx BuiltinProviderContext) []Tool {
			gotCtx = ctx
			return []Tool{{
				Name:   "custom_builtin",
				Schema: map[string]any{"type": "object"},
				Handler: func(ctx context.Context, in map[string]any) (string, error) {
					return "ok", nil
				},
			}}
		})},
	})

	if gotCtx.WorkDir != workDir {
		t.Fatalf("context workdir = %q, want %q", gotCtx.WorkDir, workDir)
	}
	if gotCtx.Shell.Binary == "" {
		t.Fatal("custom provider should receive default shell profile")
	}
	if _, ok := r.Get("custom_builtin"); !ok {
		t.Fatal("custom provider tool was not registered")
	}
	if _, ok := r.Get("read"); ok {
		t.Fatal("default builtin tools should not register when custom providers are supplied")
	}
}

func TestDefaultBuiltinProvidersCanBeComposed(t *testing.T) {
	r := NewRegistry()
	providers := append(DefaultBuiltinProviders(), builtinProviderFunc(func(ctx BuiltinProviderContext) []Tool {
		return []Tool{{
			Name:   "custom_builtin",
			Schema: map[string]any{"type": "object"},
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				return "ok", nil
			},
		}}
	}))

	RegisterBuiltins(r, BuiltinOptions{WorkDir: t.TempDir(), Providers: providers})
	if _, ok := r.Get("read"); !ok {
		t.Fatal("default builtin providers should still be registered")
	}
	if _, ok := r.Get("custom_builtin"); !ok {
		t.Fatal("custom provider tool was not registered")
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

func TestBuiltins_ExecCommandOmitsBinaryOutput(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "binary")

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

	var deltas []OutputDelta
	ctx := WithToolCallEvents(context.Background(), ToolCallEvents{
		Name:      "exec_command",
		ToolUseID: "call_binary",
		Emit: func(delta OutputDelta) {
			deltas = append(deltas, delta)
		},
	})
	out, info, err := r.CallWithInfo(ctx, "exec_command", map[string]any{"cmd": "emit binary"})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := testBinaryShellOutput()
	wantSum := sha256.Sum256(wantBytes)
	wantSHA := hex.EncodeToString(wantSum[:])
	for _, want := range []string{"[binary output omitted:", "bytes=", "sha256=" + wantSHA, "first_bytes_hex=0001504e47"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, string(wantBytes[:5])) {
		t.Fatalf("output contains raw binary prefix: %q", out)
	}
	shellResult, ok := info.StructuredResult.(ShellResult)
	if !ok {
		t.Fatalf("structured result = %T", info.StructuredResult)
	}
	if !shellResult.BinaryOmitted || shellResult.BinaryBytes != len(wantBytes) || shellResult.BinarySHA256 != wantSHA {
		t.Fatalf("shell binary metadata = %+v", shellResult)
	}
	if wantTokens := (len(shellResult.Output) + 3) / 4; shellResult.OriginalTokenCount != wantTokens {
		t.Fatalf("original token count = %d, want placeholder token count %d", shellResult.OriginalTokenCount, wantTokens)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %d, want one binary placeholder delta: %+v", len(deltas), deltas)
	}
	delta := deltas[0]
	if !delta.BinaryOmitted || delta.BinaryBytes != len(wantBytes) || delta.BinarySHA256 != wantSHA {
		t.Fatalf("delta binary metadata = %+v", delta)
	}
	if strings.Contains(delta.Text, string(wantBytes[:5])) {
		t.Fatalf("delta contains raw binary prefix: %q", delta.Text)
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
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "slow" {
		fmt.Fprintln(os.Stdout, "slow start")
		time.Sleep(3 * time.Second)
		fmt.Fprintln(os.Stdout, "slow done")
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
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "interrupt" {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		defer signal.Stop(sigCh)
		fmt.Fprintln(os.Stdout, "interrupt ready")
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stdout, "interrupted")
			os.Exit(130)
		case <-time.After(10 * time.Second):
			fmt.Fprintln(os.Stdout, "interrupt timeout")
			os.Exit(0)
		}
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "fail" {
		fmt.Fprintln(os.Stdout, "before failure stdout")
		fmt.Fprintln(os.Stderr, "before failure stderr")
		os.Exit(7)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "binary" {
		_, _ = os.Stdout.Write(testBinaryShellOutput())
		os.Exit(0)
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

func testBinaryShellOutput() []byte {
	data := []byte{0x00, 0x01, 'P', 'N', 'G'}
	for i := 0; i < 1024; i++ {
		data = append(data, byte(i%251))
	}
	return data
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

func TestRegistry_CallWithInfoReturnsParentCancellation(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name:   "soft-cancel",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "partial output", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, _, err := r.CallWithInfo(ctx, "soft-cancel", map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if out != "partial output" {
		t.Fatalf("out = %q, want partial output", out)
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

func TestRegisterBuiltinsShellToolsDisableRegistryTimeout(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 2})
	RegisterBuiltins(r, BuiltinOptions{Shell: DefaultShellProfile()})

	for _, name := range []string{"exec_command", "write_stdin"} {
		if got := r.TimeoutSecondsFor(name); got != 0 {
			t.Fatalf("%s timeout = %d, want generic timeout disabled", name, got)
		}
	}
	if got := r.TimeoutSecondsFor("list_shell_sessions"); got != 2 {
		t.Fatalf("list_shell_sessions timeout = %d, want generic timeout", got)
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

func TestRegistry_CallWithInfoRejectsMalformedRawArgumentsBeforeDispatch(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(Tool{
		Name: "echo",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			called = true
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err := r.CallWithInfo(context.Background(), "echo", map[string]any{
		"_raw_arguments": `{"value":"unterminated`,
	})
	if err == nil {
		t.Fatal("expected malformed raw arguments error")
	}
	if called {
		t.Fatal("handler was called for malformed raw arguments")
	}
	msg := err.Error()
	if !strings.Contains(msg, "provider returned malformed tool arguments") ||
		!strings.Contains(msg, "retry with smaller/chunked content") {
		t.Fatalf("error = %q, want provider malformed arguments guidance", msg)
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

func TestRegisterBuiltinsApplyPatchSchemaAndDisable(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())
	tool, ok := r.Get("apply_patch")
	if !ok {
		t.Fatal("apply_patch should be registered by default")
	}
	props, ok := tool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", tool.Schema["properties"])
	}
	if _, ok := props["patch_text"]; !ok {
		t.Fatalf("patch_text property missing from schema: %+v", props)
	}
	required, ok := tool.Schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "patch_text" {
		t.Fatalf("required = %+v, want [patch_text]", tool.Schema["required"])
	}

	disabled := NewRegistry()
	RegisterBuiltins(disabled, BuiltinOptions{WorkDir: t.TempDir(), Shell: DefaultShellProfile(), DisableApplyPatch: true})
	if _, ok := disabled.Get("apply_patch"); ok {
		t.Fatal("apply_patch should be omitted when DisableApplyPatch is set")
	}
}

func TestBuiltins_ApplyPatchAddUpdateDeleteMove(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "src.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "delete.txt"), []byte("remove me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "move.txt"), []byte("old name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	out, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: notes/new.txt",
		"+line one",
		"+line two",
		"*** Update File: src.txt",
		"@@",
		" alpha",
		"-beta",
		"+BETTER",
		" gamma",
		"*** Update File: move.txt",
		"*** Move to: moved/renamed.txt",
		"@@",
		"-old name",
		"+new name",
		"*** Delete File: delete.txt",
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"applied patch", "add=1", "update=2", "delete=1", "move=1", "notes/new.txt", "src.txt", "moved/renamed.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("apply_patch output = %q, want substring %q", out, want)
		}
	}
	if data := mustReadFile(t, filepath.Join(workDir, "notes", "new.txt")); string(data) != "line one\nline two\n" {
		t.Fatalf("new file = %q", data)
	}
	if data := mustReadFile(t, filepath.Join(workDir, "src.txt")); string(data) != "alpha\nBETTER\ngamma\n" {
		t.Fatalf("updated file = %q", data)
	}
	if _, err := os.Stat(filepath.Join(workDir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete.txt should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "move.txt")); !os.IsNotExist(err) {
		t.Fatalf("move.txt should be removed after move, stat err=%v", err)
	}
	if data := mustReadFile(t, filepath.Join(workDir, "moved", "renamed.txt")); string(data) != "new name\n" {
		t.Fatalf("moved file = %q", data)
	}
}

func TestBuiltins_ApplyPatchTrimsEnvelopeWhitespace(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": "\n \t*** Begin Patch\n*** Update File: src.txt\n@@\n-one\n+ONE\n two\n*** End Patch\n\n"})
	if err != nil {
		t.Fatal(err)
	}
	if data := mustReadFile(t, target); string(data) != "ONE\ntwo\n" {
		t.Fatalf("updated file = %q", data)
	}
}

func TestBuiltins_ApplyPatchTreatsBlankUpdateLinesAsContext(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	if err := os.WriteFile(target, []byte("alpha\n\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"@@",
		" alpha",
		"",
		"-beta",
		"+BETA",
		"",
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if data := mustReadFile(t, target); string(data) != "alpha\n\nBETA\n" {
		t.Fatalf("updated file = %q", data)
	}
}

func TestBuiltins_ApplyPatchMissingContextPreservesFile(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	original := "one\ntwo\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"@@",
		" three",
		"-missing",
		"+replacement",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "context not found") {
		t.Fatalf("err = %v, want context not found", err)
	}
	if data := mustReadFile(t, target); string(data) != original {
		t.Fatalf("file changed after failed patch: %q", data)
	}
}

func TestBuiltins_ApplyPatchAmbiguousContextPreservesFile(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	original := "repeat\nx\nrepeat\nx\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"@@",
		" repeat",
		"-x",
		"+y",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "ambiguous context") {
		t.Fatalf("err = %v, want ambiguous context", err)
	}
	if data := mustReadFile(t, target); string(data) != original {
		t.Fatalf("file changed after failed patch: %q", data)
	}
}

func TestBuiltins_ApplyPatchRejectsUnsafePath(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	unsafePatches := map[string]string{
		"parent escape": "../escape.txt",
		"windows drive": "C:/escape.txt",
		"unc slash":     "//server/share/escape.txt",
		"unc backslash": `\\server\share\escape.txt`,
	}
	for name, unsafePath := range unsafePatches {
		t.Run(name, func(t *testing.T) {
			_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
				"*** Begin Patch",
				"*** Add File: " + unsafePath,
				"+nope",
				"*** End Patch",
			}, "\n")})
			if err == nil || !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("err = %v, want unsafe path", err)
			}
		})
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(workDir), "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("escape file should not exist, stat err=%v", statErr)
	}
}

func TestBuiltins_ApplyPatchRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workDir, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: link/escape.txt",
		"+nope",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "symlink escapes workspace") {
		t.Fatalf("err = %v, want symlink escape error", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("escape file should not exist outside workspace, stat err=%v", statErr)
	}
}

func TestBuiltins_ApplyPatchRejectsMalformedPatch(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Add File: a.txt",
		"+missing envelope",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("err = %v, want missing begin patch error", err)
	}
}

func TestBuiltins_ApplyPatchRejectsDuplicateOperationsBeforeWriting(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: a.txt",
		"+first",
		"*** Add File: a.txt",
		"+second",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation") {
		t.Fatalf("err = %v, want duplicate operation", err)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "a.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("a.txt should not exist after failed patch, stat err=%v", statErr)
	}
}

func TestBuiltins_ApplyPatchValidatesWholePatchBeforeWriting(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: created.txt",
		"+created",
		"*** Update File: src.txt",
		"@@",
		" missing",
		"-line",
		"+replacement",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "context not found") {
		t.Fatalf("err = %v, want context not found", err)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("created.txt should not exist after validation failure, stat err=%v", statErr)
	}
	if data := mustReadFile(t, target); string(data) != "one\ntwo\n" {
		t.Fatalf("src.txt changed after failed patch: %q", data)
	}
}

func TestRegisterBuiltinsChunkedWriteSchema(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())
	for _, name := range []string{"write_begin", "write_chunk", "write_commit", "write_abort"} {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s should be registered", name)
		}
		if tool.Schema["type"] != "object" {
			t.Fatalf("%s schema = %+v", name, tool.Schema)
		}
	}
	begin, _ := r.Get("write_begin")
	if !strings.Contains(begin.Description, "2000 characters") || !strings.Contains(begin.Description, "4000 bytes") {
		t.Fatalf("write_begin description missing recommended chunk guidance: %q", begin.Description)
	}
	chunk, _ := r.Get("write_chunk")
	if !strings.Contains(chunk.Description, "content_omitted") || !strings.Contains(chunk.Description, "actual content string") || !strings.Contains(chunk.Description, "2000 characters") {
		t.Fatalf("write_chunk description missing provider-safe guidance: %q", chunk.Description)
	}
	if chunk.Schema["additionalProperties"] != false {
		t.Fatalf("write_chunk schema should reject additional properties: %+v", chunk.Schema)
	}
	contentSchema, ok := chunk.Schema["properties"].(map[string]any)["content"].(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(contentSchema["description"]), "2000 characters") {
		t.Fatalf("write_chunk content schema missing recommended chunk guidance: %+v", contentSchema)
	}
	if contentSchema["maxLength"] != chunkWriteRecommendedChunkChars {
		t.Fatalf("write_chunk content maxLength = %+v, want %d", contentSchema["maxLength"], chunkWriteRecommendedChunkChars)
	}

	write, _ := r.Get("write")
	if !strings.Contains(write.Description, "write_begin/write_chunk/write_commit") {
		t.Fatalf("write description should steer long content to chunked write: %q", write.Description)
	}
	writeContentSchema, ok := write.Schema["properties"].(map[string]any)["content"].(map[string]any)
	if !ok || writeContentSchema["maxLength"] != chunkWriteRecommendedChunkChars {
		t.Fatalf("write content schema should cap provider-visible content length: %+v", writeContentSchema)
	}
}

func TestBuiltins_ChunkedWriteBeginReportsRecommendedChunkSize(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())

	out, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "long.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"max_chunk_bytes=4000", "max_chunk_chars=2000", "recommended_chunk_bytes=4000", "recommended_chunk_chars=2000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("write_begin output = %q, missing %q", out, want)
		}
	}
}

func TestBuiltins_ChunkedWriteRejectsProviderUnsafeChunkSize(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())

	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "long.md"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	_, err = r.Call(context.Background(), "write_chunk", map[string]any{
		"write_id": writeID,
		"index":    0,
		"content":  strings.Repeat("x", chunkWriteMaxChunkChars+1),
	})
	if err == nil {
		t.Fatal("expected provider-unsafe chunk size to fail")
	}
	for _, want := range []string{"content exceeds max chunk limits", "2000 chars", "4000 bytes", "split into smaller chunks"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err.Error(), want)
		}
	}
}

func TestBuiltins_ChunkedWriteRejectsProjectedMetadataAsInput(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())

	_, err := r.Call(context.Background(), "write_chunk", map[string]any{
		"write_id":        "w_demo",
		"index":           0,
		"content_omitted": true,
		"content_bytes":   123,
		"content_chars":   100,
		"content_sha256":  "abc123",
	})
	if err == nil {
		t.Fatal("expected missing content error")
	}
	msg := err.Error()
	for _, want := range []string{"summary metadata", "actual content string", "2000 chars", "4000 bytes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, missing %q", msg, want)
		}
	}
}

func TestBuiltins_ChunkedWriteMalformedRawArgumentsMentionsSafeChunkSize(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())

	_, err := r.Call(context.Background(), "write_chunk", map[string]any{
		"_raw_arguments": `{"write_id":"w_demo","index":0,"content":"unterminated`,
	})
	if err == nil {
		t.Fatal("expected malformed raw arguments error")
	}
	msg := err.Error()
	for _, want := range []string{"smaller write_chunk content", "2000 chars", "4000 bytes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, missing %q", msg, want)
		}
	}
}

func TestBuiltins_ChunkedWriteCommitOverwrite(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "long.md")
	if err := os.WriteFile(target, []byte("old content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "long.md"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	for index, content := range []string{"alpha\n", "beta\n", "gamma\n"} {
		out, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": index, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, content) {
			t.Fatalf("chunk result echoed content: %q", out)
		}
	}
	full := "alpha\nbeta\ngamma\n"
	out, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 3, "sha256": sha256HexForTest(full)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "write_commit") || !strings.Contains(out, "chunks=3") {
		t.Fatalf("commit output = %q", out)
	}
	if data := mustReadFile(t, target); string(data) != full {
		t.Fatalf("target = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID}); err == nil || !strings.Contains(err.Error(), "unknown write_id") {
		t.Fatalf("commit after success err = %v, want unknown write_id", err)
	}
}

func TestBuiltins_ChunkedWriteCreateMode(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)

	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "new/report.md", "mode": "create"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "# Report\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 1}); err != nil {
		t.Fatal(err)
	}
	if data := mustReadFile(t, filepath.Join(workDir, "new", "report.md")); string(data) != "# Report\n" {
		t.Fatalf("created file = %q", data)
	}

	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "new/report.md", "mode": "create"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create existing err = %v, want already exists", err)
	}
}

func TestBuiltins_ChunkedWriteDuplicateChunkRules(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "dup.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	input := map[string]any{"write_id": writeID, "index": 0, "content": "same", "sha256": sha256HexForTest("same")}
	if _, err := r.Call(context.Background(), "write_chunk", input); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "write_chunk", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "duplicate=true") {
		t.Fatalf("duplicate output = %q, want idempotent duplicate", out)
	}
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "different"}); err == nil || !strings.Contains(err.Error(), "conflicting duplicate chunk") {
		t.Fatalf("conflict err = %v, want conflicting duplicate chunk", err)
	}
}

func TestBuiltins_ChunkedWriteValidationFailuresPreserveTarget(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "target.txt")
	original := "original\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	registerTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "target.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 1, "content": "later"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID}); err == nil || !strings.Contains(err.Error(), "missing chunk 0") {
		t.Fatalf("missing chunk err = %v, want missing chunk 0", err)
	}
	if data := mustReadFile(t, target); string(data) != original {
		t.Fatalf("target changed after missing chunk: %q", data)
	}
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "first", "sha256": "bad"}); err == nil || !strings.Contains(err.Error(), "chunk checksum mismatch") {
		t.Fatalf("chunk checksum err = %v, want mismatch", err)
	}
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 2, "sha256": "bad"}); err == nil || !strings.Contains(err.Error(), "full checksum mismatch") {
		t.Fatalf("full checksum err = %v, want mismatch", err)
	}
	if data := mustReadFile(t, target); string(data) != original {
		t.Fatalf("target changed after checksum mismatch: %q", data)
	}
}

func TestBuiltins_ChunkedWriteAbort(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "aborted.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "discard"}); err != nil {
		t.Fatal(err)
	}
	if out, err := r.Call(context.Background(), "write_abort", map[string]any{"write_id": writeID}); err != nil || !strings.Contains(out, "aborted") {
		t.Fatalf("abort out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "aborted.txt")); !os.IsNotExist(err) {
		t.Fatalf("aborted file should not exist, stat err=%v", err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID}); err == nil || !strings.Contains(err.Error(), "unknown write_id") {
		t.Fatalf("commit aborted err = %v, want unknown write_id", err)
	}
}

func TestBuiltins_ChunkedWriteStaleSession(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := newChunkWriteManager(t.TempDir())
	manager.now = func() time.Time { return now }

	session, err := manager.begin("stale.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(chunkWriteSessionTTL + time.Second)
	if _, _, _, err := manager.chunk(session.id, 0, "late", ""); err == nil || !strings.Contains(err.Error(), "stale write_id") {
		t.Fatalf("stale chunk err = %v, want stale write_id", err)
	}
	if _, _, _, err := manager.chunk(session.id, 0, "late", ""); err == nil || !strings.Contains(err.Error(), "unknown write_id") {
		t.Fatalf("second stale chunk err = %v, want unknown write_id", err)
	}
}

func TestBuiltins_ChunkedWriteRejectsConcurrentTargetSession(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"}); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("second begin err = %v, want already active", err)
	}
	if _, err := r.Call(context.Background(), "write_abort", map[string]any{"write_id": writeID}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"}); err != nil {
		t.Fatalf("begin after abort: %v", err)
	}
}

func TestBuiltins_ChunkedWriteRejectsUnsafePath(t *testing.T) {
	workDir := t.TempDir()
	r := NewRegistry()
	registerTestBuiltins(r, workDir)
	for name, unsafePath := range map[string]string{
		"parent escape": "../escape.txt",
		"windows drive": "C:/escape.txt",
		"unc slash":     "//server/share/escape.txt",
		"unc backslash": `\\server\share\escape.txt`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": unsafePath}); err == nil || !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("err = %v, want unsafe path", err)
			}
		})
	}
}

func chunkWriteIDFromResult(t *testing.T, out string) string {
	t.Helper()
	const marker = "write_id="
	start := strings.Index(out, marker)
	if start < 0 {
		t.Fatalf("output %q missing write_id", out)
	}
	start += len(marker)
	end := strings.IndexAny(out[start:], " \n")
	if end < 0 {
		return out[start:]
	}
	return out[start : start+end]
}

func sha256HexForTest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
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

func TestBuiltins_ListShellSessionsEmpty(t *testing.T) {
	r := NewRegistry()
	registerTestBuiltins(r, t.TempDir())

	out, info, err := r.CallWithInfo(context.Background(), "list_shell_sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "No running shell sessions." {
		t.Fatalf("list output = %q, want empty running message", out)
	}
	result := shellSessionListFromInfo(t, info)
	if len(result.Sessions) != 0 {
		t.Fatalf("sessions = %+v, want empty", result.Sessions)
	}
}

func TestFormatActiveShellSessionsPrompt(t *testing.T) {
	exitCode := 0
	got := FormatActiveShellSessionsPrompt([]ShellSessionInfo{
		{SessionID: 9, Running: false, ExitCode: &exitCode, Command: "completed"},
		{SessionID: 7, Running: true, TTY: true, AgeMS: 1500, IdleMS: 250, ChunkID: 3, UnreadBytes: 11, Workdir: "/tmp/work", Command: "python server.py"},
	})

	for _, want := range []string{
		"## Active Shell Sessions",
		"session_id=7",
		"running=true",
		"tty=true",
		"age=1.5s",
		"idle=250ms",
		"workdir=\"/tmp/work\"",
		"command=\"python server.py\"",
		"write_stdin",
		"list_shell_sessions",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("active shell prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "session_id=9") || strings.Contains(got, "completed") {
		t.Fatalf("completed session leaked into active prompt:\n%s", got)
	}
}

func TestFormatActiveShellSessionsPromptIsBounded(t *testing.T) {
	longCommand := strings.Repeat("c", 300)
	longWorkdir := "/" + strings.Repeat("w", 240)
	sessions := make([]ShellSessionInfo, 0, 10)
	for i := 1; i <= 10; i++ {
		sessions = append(sessions, ShellSessionInfo{
			SessionID: i,
			Running:   true,
			Command:   longCommand,
			Workdir:   longWorkdir,
		})
	}

	got := FormatActiveShellSessionsPrompt(sessions)
	if count := strings.Count(got, "\n- session_id="); count != activeShellPromptMaxSessions {
		t.Fatalf("active session rows = %d, want %d:\n%s", count, activeShellPromptMaxSessions, got)
	}
	if !strings.Contains(got, "2 more active shell session(s) omitted") {
		t.Fatalf("missing omitted count:\n%s", got)
	}
	if strings.Contains(got, longCommand) || strings.Contains(got, longWorkdir) {
		t.Fatalf("unbounded command/workdir leaked into prompt:\n%s", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("bounded prompt should show truncation marker:\n%s", got)
	}
}

func TestFormatActiveShellSessionsPromptEmpty(t *testing.T) {
	if got := FormatActiveShellSessionsPrompt(nil); got != "" {
		t.Fatalf("nil sessions prompt = %q, want empty", got)
	}
	if got := FormatActiveShellSessionsPrompt([]ShellSessionInfo{{SessionID: 1, Running: false}}); got != "" {
		t.Fatalf("completed-only prompt = %q, want empty", got)
	}
}

func TestBuiltins_ListShellSessionsRunningAndPollsReturnedSession(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "slow")
	RegisterBuiltins(r, BuiltinOptions{
		WorkDir:       workDir,
		ShellSessions: sessions,
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	_, firstInfo, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "slow one",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, secondInfo, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "slow two",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := shellResultFromInfo(t, firstInfo)
	second := shellResultFromInfo(t, secondInfo)
	if !first.Running || first.SessionID <= 0 || !second.Running || second.SessionID <= 0 {
		t.Fatalf("initial sessions = %+v / %+v, want two running sessions", first, second)
	}

	out, listInfo, err := r.CallWithInfo(context.Background(), "list_shell_sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"session_id=" + strconv.Itoa(first.SessionID),
		"session_id=" + strconv.Itoa(second.SessionID),
		"status=running",
		"slow one",
		"slow two",
		strconv.Quote(workDir),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
	list := shellSessionListFromInfo(t, listInfo)
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want two running sessions", list.Sessions)
	}
	seen := map[int]ShellSessionInfo{}
	for _, session := range list.Sessions {
		seen[session.SessionID] = session
		if !session.Running || session.ExitCode != nil || session.TimedOut {
			t.Fatalf("listed running session = %+v, want running without exit state", session)
		}
		if session.Workdir != workDir {
			t.Fatalf("listed workdir = %q, want %q", session.Workdir, workDir)
		}
		if session.StartedAt.IsZero() || session.LastAccessAt.IsZero() {
			t.Fatalf("listed times should be populated: %+v", session)
		}
		if session.AgeMS < 0 || session.IdleMS < 0 {
			t.Fatalf("listed durations should be non-negative: %+v", session)
		}
	}
	if seen[first.SessionID].Command != "slow one" || seen[second.SessionID].Command != "slow two" {
		t.Fatalf("listed commands = %+v, want original commands", seen)
	}

	out, _, err = r.CallWithInfo(context.Background(), "write_stdin", map[string]any{
		"session_id":    first.SessionID,
		"yield_time_ms": 4000,
	})
	if err != nil {
		t.Fatalf("poll returned session_id: %v\n%s", err, out)
	}
	if !strings.Contains(out, "slow done") {
		t.Fatalf("poll output = %q, want slow done", out)
	}
	out, _, err = r.CallWithInfo(context.Background(), "write_stdin", map[string]any{
		"session_id":    second.SessionID,
		"yield_time_ms": 4000,
	})
	if err != nil {
		t.Fatalf("poll second returned session_id: %v\n%s", err, out)
	}
}

func TestBuiltins_ListShellSessionsHidesCompletedByDefault(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	RegisterBuiltins(r, BuiltinOptions{
		WorkDir: t.TempDir(),
		Shell: ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	_, execInfo, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "delayed complete",
		"yield_time_ms": 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	execResult := shellResultFromInfo(t, execInfo)
	if execResult.Running {
		_, execInfo, err = r.CallWithInfo(context.Background(), "write_stdin", map[string]any{
			"session_id":    execResult.SessionID,
			"yield_time_ms": 1500,
		})
		if err != nil {
			t.Fatal(err)
		}
		execResult = shellResultFromInfo(t, execInfo)
	}
	if execResult.Running || execResult.ExitCode == nil || *execResult.ExitCode != 0 {
		t.Fatalf("exec result = %+v, want completed command", execResult)
	}

	out, info, err := r.CallWithInfo(context.Background(), "list_shell_sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "No running shell sessions." {
		t.Fatalf("default list output = %q, want no running sessions", out)
	}
	if result := shellSessionListFromInfo(t, info); len(result.Sessions) != 0 {
		t.Fatalf("default sessions = %+v, want completed hidden", result.Sessions)
	}

	out, info, err = r.CallWithInfo(context.Background(), "list_shell_sessions", map[string]any{
		"include_completed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status=exited", "exit_code=0", "delayed complete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("include_completed output missing %q:\n%s", want, out)
		}
	}
	result := shellSessionListFromInfo(t, info)
	if len(result.Sessions) != 1 {
		t.Fatalf("include_completed sessions = %+v, want one completed session", result.Sessions)
	}
	session := result.Sessions[0]
	if session.Running || session.ExitCode == nil || *session.ExitCode != 0 {
		t.Fatalf("completed session = %+v, want non-running exit 0", session)
	}
}

func TestBuiltins_ListShellSessionsPrunesCompletedAfterTTL(t *testing.T) {
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	sessions.completedTTL = time.Millisecond
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")

	result, err := sessions.Start(ShellStartRequest{
		Binary:  fakeShellProfile().Binary,
		Args:    fakeShellProfile().Args,
		Command: "delayed complete",
		Yield:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Running {
		result, err = sessions.Continue(ShellContinueRequest{
			SessionID: result.SessionID,
			Yield:     2 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if result.Running {
		t.Fatalf("continued result = %+v, want completed command", result)
	}
	time.Sleep(5 * time.Millisecond)
	if got := sessions.List(true); len(got) != 0 {
		t.Fatalf("sessions after completed TTL = %+v, want pruned", got)
	}
}

func TestBuiltins_ExecCommandRejectsTooManyActiveShellSessions(t *testing.T) {
	r := NewRegistry()
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	sessions.maxSessions = 1
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "slow")
	RegisterBuiltins(r, BuiltinOptions{
		WorkDir:       t.TempDir(),
		ShellSessions: sessions,
		Shell:         fakeShellProfile(),
	})

	_, firstInfo, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "slow one",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := shellResultFromInfo(t, firstInfo)
	if !first.Running || first.SessionID <= 0 {
		t.Fatalf("first shell result = %+v, want running session", first)
	}

	out, _, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "slow two",
		"yield_time_ms": 250,
	})
	if err == nil {
		t.Fatalf("second exec_command output = %q, want max session error", out)
	}
	if !strings.Contains(err.Error(), "too many active sessions (1)") {
		t.Fatalf("second exec_command err = %v, want max session error", err)
	}
}

func TestShellSessionManagerCloseKillsAndWaitsForSessions(t *testing.T) {
	sessions := NewShellSessionManager(context.Background())
	killed := make(chan struct{})
	done := make(chan struct{})
	closed := make(chan struct{})
	session := &shellSession{
		id:         1,
		started:    time.Now(),
		lastAccess: time.Now(),
		doneChan:   done,
		killFunc: func() error {
			close(killed)
			return nil
		},
	}
	sessions.sessions[session.id] = session

	go func() {
		_ = sessions.Close()
		close(closed)
	}()

	select {
	case <-killed:
	case <-time.After(time.Second):
		t.Fatal("Close did not call kill on the running shell session")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before the shell session reported done")
	default:
	}
	close(done)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the shell session reported done")
	}
}

type timedOutStructuredTestResult struct{}

func (timedOutStructuredTestResult) ToolCallTimedOut() bool {
	return true
}

type exitCodeStructuredTestResult struct {
	code int
}

func (r exitCodeStructuredTestResult) ToolCallExitCode() (int, bool) {
	return r.code, true
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
	if info.Observation == nil {
		t.Fatal("observation = nil")
	}
	if info.Observation.ToolName != "structured" || info.Observation.Content != "ok" {
		t.Fatalf("observation = %+v, want structured tool output", info.Observation)
	}
	obsStructured, ok := info.Observation.StructuredResult.(map[string]any)
	if !ok || obsStructured["answer"] != 42 {
		t.Fatalf("observation structured result = %#v, want answer 42", info.Observation.StructuredResult)
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
	if info.Observation == nil || !info.Observation.TimedOut || info.Observation.Error == "" {
		t.Fatalf("observation = %+v, want timed-out error observation", info.Observation)
	}
}

func TestRegistryCallWithInfoClassifiesDirectDeadlineExceeded(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 1})
	if err := r.Register(Tool{
		Name:   "deadline",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, input map[string]any) (string, error) {
			return "partial output", context.DeadlineExceeded
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "deadline", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "partial output" {
		t.Fatalf("out = %q, want partial output", out)
	}
	if !info.TimedOut || info.ErrorKind != "timeout" {
		t.Fatalf("info = %+v, want timeout classification", info)
	}
	if !strings.Contains(info.RawCause, "context deadline exceeded") {
		t.Fatalf("raw cause = %q, want original deadline cause", info.RawCause)
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %q, should use public timeout wording", err.Error())
	}
	if !strings.Contains(err.Error(), "tools: deadline timed out after 1s") {
		t.Fatalf("err = %q, want public tool timeout", err.Error())
	}
	if info.Observation == nil || !info.Observation.TimedOut || info.Observation.ErrorKind != "timeout" {
		t.Fatalf("observation = %+v, want timeout observation", info.Observation)
	}
	if !strings.Contains(info.Observation.RawCause, "context deadline exceeded") {
		t.Fatalf("observation raw cause = %q, want original deadline cause", info.Observation.RawCause)
	}
}

func TestRegistryCallWithInfoObservationCapturesStructuredExitCode(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name:   "check",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, input map[string]any) (Result, error) {
			return Result{
				Text:       "opaque failure output",
				Structured: exitCodeStructuredTestResult{code: 9},
			}, errors.New("check failed")
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "check", map[string]any{"path": "artifact.txt"})
	if err == nil {
		t.Fatal("expected check error")
	}
	if out != "opaque failure output" {
		t.Fatalf("out = %q, want opaque failure output", out)
	}
	if info.Observation == nil {
		t.Fatal("observation = nil")
	}
	if info.Observation.ExitCode == nil || *info.Observation.ExitCode != 9 {
		t.Fatalf("observation exit code = %+v, want 9", info.Observation.ExitCode)
	}
	if info.Observation.Error != "check failed" {
		t.Fatalf("observation error = %q, want check failed", info.Observation.Error)
	}
}

func TestObservationWithRuntimeContextPreservesSpecificExitCode(t *testing.T) {
	explicitCode := 42
	tests := []struct {
		name string
		obs  Observation
		want int
	}{
		{
			name: "explicit option",
			obs:  NewObservation(ObservationOptions{ExitCode: &explicitCode}),
			want: 42,
		},
		{
			name: "structured result",
			obs: NewObservation(ObservationOptions{
				StructuredResult: exitCodeStructuredTestResult{code: 9},
			}),
			want: 9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.obs.WithRuntimeContext(
				"exec_command",
				"call_1",
				map[string]any{"cmd": "false"},
				"runtime output",
				&ShellExitError{ToolName: "exec_command", Code: 7},
			)
			if got.ExitCode == nil || *got.ExitCode != tt.want {
				t.Fatalf("exit code = %+v, want %d", got.ExitCode, tt.want)
			}
			if got.Error == "" {
				t.Fatal("error should still be captured from runtime context")
			}
			if got.ToolName != "exec_command" || got.ToolUseID != "call_1" {
				t.Fatalf("runtime identity = %q/%q, want exec_command/call_1", got.ToolName, got.ToolUseID)
			}
		})
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

func TestBuiltins_WriteStdinInterruptsNonTTYSession(t *testing.T) {
	r := NewRegistry()
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "interrupt")
	RegisterBuiltins(r, BuiltinOptions{
		WorkDir:       t.TempDir(),
		ShellSessions: sessions,
		Shell:         fakeShellProfile(),
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "interrupt",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionIDFromOutput(t, out)
	if !strings.Contains(out, "interrupt ready") {
		t.Fatalf("initial output = %q, want interrupt ready", out)
	}

	out, err = r.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"chars":         shellInterruptInput,
		"yield_time_ms": 1500,
	})
	if err == nil {
		t.Fatalf("write_stdin output = %q, want interrupted exit error", out)
	}
	if !strings.Contains(out, "Process exited with code") {
		t.Fatalf("interrupted output = %q, want exited status", out)
	}
	if runtime.GOOS != "windows" && !strings.Contains(out, "interrupted") {
		t.Fatalf("interrupted output = %q, want signal handler output", out)
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

func TestBuiltins_WriteStdinInterruptsTTYSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows tty coverage runs through ConPTY-specific tests")
	}
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "interrupt")
	RegisterBuiltins(r, BuiltinOptions{
		Shell: fakeShellProfile(),
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "interrupt",
		"tty":           true,
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionIDFromOutput(t, out)
	if !strings.Contains(out, "interrupt ready") {
		t.Fatalf("initial tty output = %q, want interrupt ready", out)
	}

	out, err = r.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    sessionID,
		"chars":         shellInterruptInput,
		"yield_time_ms": 1500,
	})
	if err == nil {
		t.Fatalf("write_stdin output = %q, want interrupted exit error", out)
	}
	if !strings.Contains(out, "interrupted") {
		t.Fatalf("interrupted tty output = %q, want signal handler output", out)
	}
	if !strings.Contains(out, "Process exited with code") {
		t.Fatalf("interrupted tty output = %q, want exited status", out)
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

func TestBuiltins_ShellYieldIsNotGenericToolTimeout(t *testing.T) {
	r := NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "slow")
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
		"cmd":           "ignored by fake shell",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatalf("exec_command should yield without generic timeout: %v\n%s", err, out)
	}
	if info.TimeoutSeconds != 0 || info.TimedOut {
		t.Fatalf("exec info = %+v, want shell without generic timeout", info)
	}
	if !strings.Contains(out, "slow start") || strings.Contains(out, "slow done") {
		t.Fatalf("initial output = %q, want only slow start", out)
	}
	first := shellResultFromInfo(t, info)
	if !first.Running || first.SessionID <= 0 || first.TimedOut {
		t.Fatalf("initial shell result = %+v, want running non-timeout session", first)
	}

	out, info, err = r.CallWithInfo(context.Background(), "write_stdin", map[string]any{
		"session_id":    first.SessionID,
		"yield_time_ms": 1500,
	})
	if err != nil {
		t.Fatalf("empty write_stdin poll should yield without generic timeout: %v\n%s", err, out)
	}
	if info.TimeoutSeconds != 0 || info.TimedOut {
		t.Fatalf("poll info = %+v, want shell without generic timeout", info)
	}
	poll := shellResultFromInfo(t, info)
	if poll.Running || poll.ExitCode == nil || *poll.ExitCode != 0 || poll.TimedOut {
		t.Fatalf("poll shell result = %+v, want successful non-timeout completion", poll)
	}
	if !strings.Contains(out, "slow done") {
		t.Fatalf("poll output = %q, want slow done", out)
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
	want := []string{"apply_patch", "edit", "exec_command", "grep", "list_shell_sessions", "read", "write", "write_abort", "write_begin", "write_chunk", "write_commit", "write_stdin"}
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

	listProps := schemaProperties(t, byName["list_shell_sessions"])
	if _, ok := listProps["include_completed"]; !ok {
		t.Fatalf("list_shell_sessions schema missing include_completed: %+v", listProps)
	}
	if _, ok := listProps["timeout"]; ok {
		t.Fatalf("list_shell_sessions schema should not expose runtime timeout: %+v", listProps)
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

func shellSessionListFromInfo(t *testing.T, info CallInfo) ShellSessionListResult {
	t.Helper()
	result, ok := info.StructuredResult.(ShellSessionListResult)
	if !ok {
		t.Fatalf("structured result = %#v, want ShellSessionListResult", info.StructuredResult)
	}
	return result
}
