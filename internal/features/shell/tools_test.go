package shell

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
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestBuiltins_ExecCommandUsesConfiguredProfileAndWorkdir(t *testing.T) {
	r := toolcore.NewRegistry()
	workDir := t.TempDir()
	callDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "shell.json")
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MARKER", marker)

	registerTestTools(r, testToolOptions{
		WorkDir: workDir,
		Shell: command.ShellProfile{
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

func TestBuiltinsExecCommandPropagatesResolvedEnvironment(t *testing.T) {
	snapshot, err := environment.Resolve(environment.Options{Layers: []environment.Layer{{
		Source: environment.SourceDotenv,
		Path:   "/work/.env",
		Values: map[string]string{
			"JUEX_FAKE_SHELL":          "1",
			"JUEX_FAKE_SHELL_MODE":     "environment",
			"SHELL_RUNTIME_ENV_MARKER": "from-snapshot",
		},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{
		WorkDir:     t.TempDir(),
		Environment: snapshot,
		Shell:       shellToolstestFakeShellProfile(),
	})
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-snapshot") {
		t.Fatalf("output = %q", out)
	}
}

func TestBuiltins_ExecCommandRelativeWorkdirResolvesFromWorkDir(t *testing.T) {
	r := toolcore.NewRegistry()
	workDir := t.TempDir()
	relativeDir := "nested"
	wantDir := filepath.Join(workDir, relativeDir)
	if err := os.MkdirAll(wantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "shell.json")
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MARKER", marker)

	registerTestTools(r, testToolOptions{
		WorkDir: workDir,
		Shell: command.ShellProfile{
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
	r := toolcore.NewRegistry()
	workDir := t.TempDir()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "binary")

	registerTestTools(r, testToolOptions{
		WorkDir: workDir,
		Shell: command.ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})

	var deltas []toolcore.OutputDelta
	ctx := toolcore.WithToolCallEvents(context.Background(), toolcore.ToolCallEvents{
		Name:      "exec_command",
		ToolUseID: "call_binary",
		Emit: func(delta toolcore.OutputDelta) {
			deltas = append(deltas, delta)
		},
	})
	out, info, err := r.CallWithInfo(ctx, "exec_command", map[string]any{"cmd": "emit binary"})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := shellToolstestTestBinaryShellOutput()
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
	shellResult, ok := info.StructuredResult.(toolcore.CommandResult)
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
		_, _ = os.Stdout.Write(shellToolstestTestBinaryShellOutput())
		os.Exit(0)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "environment" {
		fmt.Fprintln(os.Stdout, os.Getenv("SHELL_RUNTIME_ENV_MARKER"))
		os.Exit(0)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "environment-delayed" {
		time.Sleep(500 * time.Millisecond)
		fmt.Fprintln(os.Stdout, os.Getenv("SHELL_RUNTIME_ENV_MARKER"))
		os.Exit(0)
	}
	if os.Getenv("JUEX_FAKE_SHELL_MODE") == "tty" {
		fmt.Fprintf(os.Stdout, "stdin_tty:%t stdout_tty:%t stderr_tty:%t\n", shellToolstestIsCharDevice(os.Stdin), shellToolstestIsCharDevice(os.Stdout), shellToolstestIsCharDevice(os.Stderr))
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

func TestRegisterBuiltinsShellToolsDisableRegistryTimeout(t *testing.T) {
	r := toolcore.NewRegistryWithOptions(toolcore.RegistryOptions{DefaultTimeoutSeconds: 2})
	registerTestTools(r, testToolOptions{Shell: command.DefaultShellProfile()})

	for _, name := range []string{"exec_command", "write_stdin"} {
		if got := r.TimeoutSecondsFor(name); got != 0 {
			t.Fatalf("%s timeout = %d, want generic timeout disabled", name, got)
		}
	}
	if got := r.TimeoutSecondsFor("list_shell_sessions"); got != 2 {
		t.Fatalf("list_shell_sessions timeout = %d, want generic timeout", got)
	}
}

func TestBuiltins_ExecCommandAcceptsRawArgumentsFallback(t *testing.T) {
	r := toolcore.NewRegistry()
	shellToolstestRegisterTestBuiltins(r, "")

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

func TestBuiltins_ExecCommand(t *testing.T) {
	r := toolcore.NewRegistry()
	shellToolstestRegisterTestBuiltins(r, "")
	out, info, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{"cmd": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	result := shellToolstestShellResultFromInfo(t, info)
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "fail")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
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
	result := shellToolstestShellResultFromInfo(t, info)
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("shell result exit code = %+v, want 7", result.ExitCode)
	}
	if code, ok := toolcore.ExitCodeFromError(err); !ok || code != 7 {
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
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
	initialResult := shellToolstestShellResultFromInfo(t, info)
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
	continuedResult := shellToolstestShellResultFromInfo(t, info)
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
	r := toolcore.NewRegistry()
	shellToolstestRegisterTestBuiltins(r, t.TempDir())

	out, info, err := r.CallWithInfo(context.Background(), "list_shell_sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "No running shell sessions." {
		t.Fatalf("list output = %q, want empty running message", out)
	}
	result := shellToolstestShellSessionListFromInfo(t, info)
	if len(result.Sessions) != 0 {
		t.Fatalf("sessions = %+v, want empty", result.Sessions)
	}
}

func TestBuiltins_ExecCommandSandboxDisabledDoesNotWrap(t *testing.T) {
	r := toolcore.NewRegistry()
	runner := &shellToolstestFakeSandboxRunner{err: errors.New("should not be called")}
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "instant")
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		Shell:         shellToolstestFakeShellProfile(),
		Sandbox:       sandbox.DisabledPolicy(),
		SandboxRunner: runner,
	})

	out, _, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{"cmd": "hello"})
	if err != nil {
		t.Fatalf("exec_command failed: %v\n%s", err, out)
	}
	if runner.calls != 0 {
		t.Fatalf("sandbox runner calls = %d, want 0 when disabled", runner.calls)
	}
}

func TestBuiltins_ExecCommandSandboxEnabledWrapsBeforeStart(t *testing.T) {
	r := toolcore.NewRegistry()
	runner := &shellToolstestFakeSandboxRunner{}
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "instant")
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		Shell:         shellToolstestFakeShellProfile(),
		Sandbox:       policy,
		SandboxRunner: runner,
	})

	out, _, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{"cmd": "hello"})
	if err != nil {
		t.Fatalf("exec_command failed: %v\n%s", err, out)
	}
	if runner.calls != 1 {
		t.Fatalf("sandbox runner calls = %d, want 1", runner.calls)
	}
	if len(runner.specs) != 1 || runner.specs[0].Binary != os.Args[0] || !shellToolstestContainsString(runner.specs[0].Args, "hello") {
		t.Fatalf("sandbox runner specs = %+v", runner.specs)
	}
}

func TestBuiltins_ExecCommandGrantsAgentStateDir(t *testing.T) {
	workDir := t.TempDir()
	agentStateDir := filepath.Join(t.TempDir(), "agents", "abcdef")
	if err := os.MkdirAll(agentStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(agentStateDir, "media")
	if err := os.Mkdir(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &shellToolstestFakeSandboxRunner{}
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		AgentStateDir: agentStateDir,
		MediaDir:      mediaDir,
		Shell:         shellToolstestFakeShellProfile(),
		Sandbox:       policy,
		SandboxRunner: runner,
	})

	if _, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": "ok"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("sandbox requests = %d", len(runner.requests))
	}
	if got := runner.requests[0].FilePolicy.WritableRoots(); len(got) != 2 || got[0] != shellToolstestCanonicalPathForTest(t, workDir) || got[1] != shellToolstestCanonicalPathForTest(t, agentStateDir) {
		t.Fatalf("writable roots = %#v, want Workspace and AgentStateDir", got)
	}
	if got := runner.requests[0].FilePolicy.ReadOnlyRoots(); len(got) != 1 || got[0] != shellToolstestCanonicalPathForTest(t, mediaDir) {
		t.Fatalf("read-only roots = %#v, want media root %q", got, shellToolstestCanonicalPathForTest(t, mediaDir))
	}
}

func TestBuiltins_ExecCommandDoesNotCreateAgentStateDir(t *testing.T) {
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "instant")
	agentStateDir := filepath.Join(t.TempDir(), "agents", "abcdef")
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		AgentStateDir: agentStateDir,
		Shell:         shellToolstestFakeShellProfile(),
	})
	if _, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentStateDir); !os.IsNotExist(err) {
		t.Fatalf("exec_command created AgentStateDir: %v", err)
	}
}

func TestBuiltins_ExecCommandSandboxErrorDoesNotStartCommand(t *testing.T) {
	r := toolcore.NewRegistry()
	runner := &shellToolstestFakeSandboxRunner{err: errors.New("sandbox denied")}
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "instant")
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		Shell:         shellToolstestFakeShellProfile(),
		Sandbox:       policy,
		SandboxRunner: runner,
	})

	out, _, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{"cmd": "hello"})
	if err == nil || !strings.Contains(err.Error(), "sandbox denied") {
		t.Fatalf("err = %v, output=%q; want sandbox error", err, out)
	}
	if runner.calls != 1 {
		t.Fatalf("sandbox runner calls = %d, want 1", runner.calls)
	}
	if strings.Contains(out, "instant done") {
		t.Fatalf("command appears to have started despite sandbox error: %q", out)
	}
}

func TestBuiltins_ExecCommandCanceledDuringSandboxPrepareDoesNotStartCommand(t *testing.T) {
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "instant")
	ctx, cancel := context.WithCancel(context.Background())
	runner := &shellToolstestFakeSandboxRunner{
		prepare: func(_ context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
			cancel()
			return req.Spec, nil
		},
	}
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		Shell:         shellToolstestFakeShellProfile(),
		Sandbox:       policy,
		SandboxRunner: runner,
	})
	out, _, err := r.CallWithInfo(ctx, "exec_command", map[string]any{"cmd": "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("exec_command error = %v, output = %q; want context canceled", err, out)
	}
	if runner.calls != 1 {
		t.Fatalf("sandbox runner calls = %d, want 1", runner.calls)
	}
	if strings.Contains(out, "instant done") {
		t.Fatalf("command started after cancellation during sandbox prepare: %q", out)
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
	r := toolcore.NewRegistry()
	workDir := t.TempDir()
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "slow")
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		ShellSessions: sessions,
		Shell: command.ShellProfile{
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
	first := shellToolstestShellResultFromInfo(t, firstInfo)
	second := shellToolstestShellResultFromInfo(t, secondInfo)
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
	list := shellToolstestShellSessionListFromInfo(t, listInfo)
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	registerTestTools(r, testToolOptions{
		WorkDir: t.TempDir(),
		Shell: command.ShellProfile{
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
	execResult := shellToolstestShellResultFromInfo(t, execInfo)
	if execResult.Running {
		_, execInfo, err = r.CallWithInfo(context.Background(), "write_stdin", map[string]any{
			"session_id":    execResult.SessionID,
			"yield_time_ms": 1500,
		})
		if err != nil {
			t.Fatal(err)
		}
		execResult = shellToolstestShellResultFromInfo(t, execInfo)
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
	if result := shellToolstestShellSessionListFromInfo(t, info); len(result.Sessions) != 0 {
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
	result := shellToolstestShellSessionListFromInfo(t, info)
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
		Binary:  shellToolstestFakeShellProfile().Binary,
		Args:    shellToolstestFakeShellProfile().Args,
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
	r := toolcore.NewRegistry()
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	sessions.maxSessions = 1
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "slow")
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		ShellSessions: sessions,
		Shell:         shellToolstestFakeShellProfile(),
	})

	_, firstInfo, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "slow one",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := shellToolstestShellResultFromInfo(t, firstInfo)
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

func TestShellSessionWaitsForOutputPumpBeforeCompletion(t *testing.T) {
	processExited := make(chan struct{})
	outputDone := make(chan struct{})
	session := &shellSession{
		started:    time.Now(),
		outputDone: outputDone,
		doneChan:   make(chan struct{}),
		waitFunc: func() error {
			close(processExited)
			return nil
		},
	}
	go session.wait(context.Background())

	select {
	case <-processExited:
	case <-time.After(time.Second):
		t.Fatal("process wait did not complete")
	}
	select {
	case <-session.doneChan:
		t.Fatal("session completed before the output pump drained")
	default:
	}

	session.appendOutput([]byte("final tty output\n"))
	close(outputDone)
	select {
	case <-session.doneChan:
	case <-time.After(time.Second):
		t.Fatal("session did not complete after the output pump drained")
	}
	result := session.snapshot(true, defaultShellMaxOutputTokens)
	if result.Running || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("session result = %+v, want completed success", result)
	}
	if result.Output != "final tty output\n" {
		t.Fatalf("session output = %q, want drained final output", result.Output)
	}
}

func TestShellSessionSnapshotWaitsForPendingOutputDelta(t *testing.T) {
	emitStarted := make(chan struct{})
	releaseEmit := make(chan struct{})
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: toolcore.DefaultCommandOutputBytes,
		events: toolcore.ToolCallEvents{
			Name:      "exec_command",
			ToolUseID: "tool-1",
			Emit: func(toolcore.OutputDelta) {
				close(emitStarted)
				<-releaseEmit
			},
		},
	}
	appendDone := make(chan struct{})
	go func() {
		session.appendOutput([]byte("tail output\n"))
		close(appendDone)
	}()

	select {
	case <-emitStarted:
	case <-time.After(time.Second):
		t.Fatal("output delta emission did not start")
	}
	snapshotDone := make(chan ShellSessionResult, 1)
	go func() {
		snapshotDone <- session.snapshot(true, defaultShellMaxOutputTokens)
	}()

	select {
	case result := <-snapshotDone:
		close(releaseEmit)
		<-appendDone
		t.Fatalf("snapshot returned before the pending output delta: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseEmit)
	select {
	case <-appendDone:
	case <-time.After(time.Second):
		t.Fatal("output append did not finish after delta emission")
	}
	select {
	case result := <-snapshotDone:
		if result.Output != "tail output\n" {
			t.Fatalf("snapshot output = %q, want emitted tail output", result.Output)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot did not finish after delta emission")
	}
}

func TestBuiltins_ExecCommandTTYWritesStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows tty coverage runs through ConPTY-specific tests")
	}
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "stdin")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
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
	sessionID := shellToolstestSessionIDFromOutput(t, out)

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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "confirm")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
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
	sessionID := shellToolstestSessionIDFromOutput(t, out)
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
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
	sessionID := shellToolstestSessionIDFromOutput(t, out)

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
	r := toolcore.NewRegistry()
	sessions := NewShellSessionManager(context.Background())
	defer func() {
		_ = sessions.Close()
	}()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "interrupt")
	registerTestTools(r, testToolOptions{
		WorkDir:       t.TempDir(),
		ShellSessions: sessions,
		Shell:         shellToolstestFakeShellProfile(),
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "interrupt",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := shellToolstestSessionIDFromOutput(t, out)
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "tty")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
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
	sessionID := shellToolstestSessionIDFromOutput(t, out)
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "interrupt")
	registerTestTools(r, testToolOptions{
		Shell: shellToolstestFakeShellProfile(),
	})

	out, err := r.Call(context.Background(), "exec_command", map[string]any{
		"cmd":           "interrupt",
		"tty":           true,
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := shellToolstestSessionIDFromOutput(t, out)
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
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
	})
	deltas := make(chan toolcore.OutputDelta, 10)
	ctx := toolcore.WithToolCallEvents(context.Background(), toolcore.ToolCallEvents{
		Name:      "exec_command",
		ToolUseID: "tool-1",
		Emit: func(delta toolcore.OutputDelta) {
			deltas <- delta
		},
	})

	if _, err := r.Call(ctx, "exec_command", map[string]any{
		"cmd":           "delayed",
		"yield_time_ms": 250,
	}); err != nil {
		t.Fatal(err)
	}
	var delta toolcore.OutputDelta
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

func TestBuiltins_WriteStdinOwnsItsLiveOutputDeltas(t *testing.T) {
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "delayed")
	registerTestTools(r, testToolOptions{Shell: shellToolstestFakeShellProfile()})

	var mu sync.Mutex
	var deltas []toolcore.OutputDelta
	emit := func(delta toolcore.OutputDelta) {
		mu.Lock()
		defer mu.Unlock()
		deltas = append(deltas, delta)
	}
	execCtx := toolcore.WithToolCallEvents(context.Background(), toolcore.ToolCallEvents{
		Name: "exec_command", ToolUseID: "exec-call", Emit: emit,
	})
	_, info, err := r.CallWithInfo(execCtx, "exec_command", map[string]any{
		"cmd": "delayed", "yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := shellToolstestShellResultFromInfo(t, info)
	if !initial.Running {
		t.Fatalf("initial shell result = %+v, want running", initial)
	}

	stdinCtx := toolcore.WithToolCallEvents(context.Background(), toolcore.ToolCallEvents{
		Name: "write_stdin", ToolUseID: "stdin-call", Emit: emit,
	})
	if _, _, err := r.CallWithInfo(stdinCtx, "write_stdin", map[string]any{
		"session_id": initial.SessionID, "yield_time_ms": 800,
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	var sawExec, sawStdin bool
	for _, delta := range deltas {
		switch delta.ToolUseID {
		case "exec-call":
			sawExec = sawExec || strings.Contains(delta.Text, "first chunk")
		case "stdin-call":
			sawStdin = sawStdin || strings.Contains(delta.Text, "second chunk")
		default:
			t.Fatalf("delta used stale tool identity: %+v", delta)
		}
	}
	if !sawExec || !sawStdin {
		t.Fatalf("deltas = %+v, want output bound to both invocations", deltas)
	}
}

func TestBuiltins_ShellYieldIsNotGenericToolTimeout(t *testing.T) {
	r := toolcore.NewRegistry()
	t.Setenv("JUEX_FAKE_SHELL", "1")
	t.Setenv("JUEX_FAKE_SHELL_MODE", "slow")
	registerTestTools(r, testToolOptions{
		Shell: command.ShellProfile{
			Profile:   "fake",
			Family:    "posix",
			Binary:    os.Args[0],
			Args:      []string{"-test.run=TestShellHelperProcess", "--"},
			PathStyle: "posix",
		},
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
	first := shellToolstestShellResultFromInfo(t, info)
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
	poll := shellToolstestShellResultFromInfo(t, info)
	if poll.Running || poll.ExitCode == nil || *poll.ExitCode != 0 || poll.TimedOut {
		t.Fatalf("poll shell result = %+v, want successful non-timeout completion", poll)
	}
	if !strings.Contains(out, "slow done") {
		t.Fatalf("poll output = %q, want slow done", out)
	}
}

func TestBuiltins_ExecCommandWorkdir(t *testing.T) {
	r := toolcore.NewRegistry()
	shellToolstestRegisterTestBuiltins(r, "")
	dir := t.TempDir()
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": shellToolstestPwdCommand(), "workdir": dir})
	if err != nil {
		t.Fatal(err)
	}
	// On macOS /tmp is a symlink to /private/tmp so just check the basename.
	if !strings.Contains(out, filepath.Base(dir)) {
		t.Fatalf("expected pwd output to contain %s, got %q", dir, out)
	}
}

func TestBuiltins_ExecCommandDefaultsToWorkDir(t *testing.T) {
	r := toolcore.NewRegistry()
	dir := t.TempDir()
	shellToolstestRegisterTestBuiltins(r, dir)
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": shellToolstestPwdCommand()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Base(dir)) {
		t.Fatalf("shell defaulted to %q, want under %s", out, dir)
	}
}

func TestBuiltins_ExecCommandWorkdirOverridesWorkDir(t *testing.T) {
	// Explicit workdir in the call wins over the configured WorkDir.
	r := toolcore.NewRegistry()
	work := t.TempDir()
	other := t.TempDir()
	shellToolstestRegisterTestBuiltins(r, work)
	out, err := r.Call(context.Background(), "exec_command", map[string]any{"cmd": shellToolstestPwdCommand(), "workdir": other})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Base(other)) {
		t.Fatalf("expected cwd to win, got %q", out)
	}
}

func TestBuiltinSchemas_ExecCommandAndWriteStdinShape(t *testing.T) {
	r := toolcore.NewRegistry()
	shellToolstestRegisterTestBuiltins(r, "")
	specs := r.Specs()
	byName := map[string]map[string]any{}
	for _, spec := range specs {
		byName[spec.Name] = spec.Schema
	}

	execProps := shellToolstestSchemaProperties(t, byName["exec_command"])
	for _, want := range []string{"cmd", "workdir", "tty", "yield_time_ms", "max_output_tokens"} {
		if _, ok := execProps[want]; !ok {
			t.Fatalf("exec_command schema missing %q: %+v", want, execProps)
		}
	}
	if _, ok := execProps["timeout"]; ok {
		t.Fatalf("exec_command schema should not expose runtime timeout: %+v", execProps)
	}
	if _, ok := execProps["cwd"]; ok {
		t.Fatalf("exec_command schema exposes unexpected cwd: %+v", execProps)
	}

	stdinProps := shellToolstestSchemaProperties(t, byName["write_stdin"])
	for _, want := range []string{"session_id", "chars", "yield_time_ms", "max_output_tokens"} {
		if _, ok := stdinProps[want]; !ok {
			t.Fatalf("write_stdin schema missing %q: %+v", want, stdinProps)
		}
	}
	if _, ok := stdinProps["timeout"]; ok {
		t.Fatalf("write_stdin schema should not expose runtime timeout: %+v", stdinProps)
	}
	if _, ok := stdinProps["stdin"]; ok {
		t.Fatalf("write_stdin schema exposes unexpected stdin: %+v", stdinProps)
	}
	sessionIDSchema, _ := stdinProps["session_id"].(map[string]any)
	if sessionIDSchema["type"] != "integer" {
		t.Fatalf("write_stdin session_id schema = %+v, want integer", sessionIDSchema)
	}

	listProps := shellToolstestSchemaProperties(t, byName["list_shell_sessions"])
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

func shellToolstestCanonicalPathForTest(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func shellToolstestContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type shellToolstestFakeSandboxRunner struct {
	calls    int
	specs    []sandbox.ExecSpec
	requests []sandbox.Request
	err      error
	prepare  func(context.Context, sandbox.Request) (sandbox.ExecSpec, error)
}

func shellToolstestFakeShellProfile() command.ShellProfile {
	return command.ShellProfile{
		Profile:   "fake",
		Family:    "posix",
		Binary:    os.Args[0],
		Args:      []string{"-test.run=TestShellHelperProcess", "--"},
		PathStyle: "posix",
	}
}

func shellToolstestIsCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func shellToolstestPwdCommand() string {
	if runtime.GOOS == "windows" {
		return "cd"
	}
	return "pwd"
}

func shellToolstestRegisterTestBuiltins(r *toolcore.Registry, workDir string) {
	registerTestTools(r, testToolOptions{WorkDir: workDir, MediaDir: shellToolstestTestMediaDir(workDir), Shell: command.DefaultShellProfile()})
}

func shellToolstestSchemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %+v", schema)
	}
	return props
}

func shellToolstestSessionIDFromOutput(t *testing.T, out string) int {
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

func shellToolstestShellResultFromInfo(t *testing.T, info toolcore.CallInfo) toolcore.CommandResult {
	t.Helper()
	result, ok := info.StructuredResult.(toolcore.CommandResult)
	if !ok {
		t.Fatalf("structured result = %#v, want ShellResult", info.StructuredResult)
	}
	return result
}

func shellToolstestShellSessionListFromInfo(t *testing.T, info toolcore.CallInfo) ShellSessionListResult {
	t.Helper()
	result, ok := info.StructuredResult.(ShellSessionListResult)
	if !ok {
		t.Fatalf("structured result = %#v, want ShellSessionListResult", info.StructuredResult)
	}
	return result
}

func shellToolstestTestBinaryShellOutput() []byte {
	data := []byte{0x00, 0x01, 'P', 'N', 'G'}
	for i := 0; i < 1024; i++ {
		data = append(data, byte(i%251))
	}
	return data
}

func shellToolstestTestMediaDir(workDir string) string { return filepath.Join(workDir, "media") }

func (r *shellToolstestFakeSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
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
