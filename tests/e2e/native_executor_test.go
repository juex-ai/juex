//go:build linux || darwin

package e2e

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeExecutorConcurrentDeliveryRunsOnce(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	request := nativeRequest(t, "delivered-many-times", "exec_command", native.CommandArguments{Command: "printf once >> counter"})
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			if _, err := engine.Submit(request); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	result := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() })
	if result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "counter"))
	if err != nil || string(data) != "once" {
		t.Fatal("concurrent delivery repeated execution", string(data), err)
	}
}

func TestNativeExecutorMCPPersistsNotificationsAndCallIdentity(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	connection := nativeRequest(t, "mcp-connection", "mcp_connect", native.MCPArguments{Command: os.Args[0], Args: []string{"-test.run=^TestNativeExecutorMCPHelper$"}, Environment: map[string]string{"JUEX_NATIVE_MCP_HELPER": "1", "JUEX_NATIVE_MCP_COUNTER": filepath.Join(config.WorkingDirectory, "mcp-counter")}})
	if _, err := engine.Submit(connection); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, connection.ID, func(snapshot execprotocol.Snapshot) bool {
		return strings.Contains(snapshot.Text(), `"type":"connected"`)
	})
	list := nativeRun(t, engine, nativeRequest(t, "mcp-tools", "mcp_list", native.MCPArguments{ConnectionID: connection.ID}))
	if list.State != execprotocol.Completed || !strings.Contains(list.Text(), `"name":"echo"`) {
		t.Fatal(list)
	}
	call := nativeRequest(t, "mcp-call-once", "mcp_call", native.MCPArguments{ConnectionID: connection.ID, Name: "echo", Arguments: map[string]any{"text": "hello"}})
	for range 2 {
		result := nativeRun(t, engine, call)
		if result.State != execprotocol.Completed || !strings.Contains(result.Text(), "echo:hello") {
			t.Fatal(result)
		}
	}
	notification := nativeEventually(t, engine, connection.ID, func(snapshot execprotocol.Snapshot) bool { return strings.Contains(snapshot.Text(), "after-call") })
	if notification.State != execprotocol.Running {
		t.Fatal("connection did not outlive tool calls", notification)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "mcp-counter"))
	if err != nil || string(data) != "call" {
		t.Fatal("MCP side effect repeated", string(data), err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine = openNative(t, config)
	retained, err := engine.Snapshot("agent-one", connection.ID, 0, 64<<10)
	if err != nil || !strings.Contains(retained.Text(), "after-call") || retained.State != execprotocol.Cancelled {
		t.Fatal(retained, err)
	}
}

func TestNativeExecutorMCPHelper(t *testing.T) {
	if os.Getenv("JUEX_NATIVE_MCP_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	writer := json.NewEncoder(os.Stdout)
	for reader.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": request.Params["protocolVersion"], "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "native-fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Fixture echo", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			file, err := os.OpenFile(os.Getenv("JUEX_NATIVE_MCP_COUNTER"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(3)
			}
			if _, err := file.WriteString("call"); err != nil {
				os.Exit(4)
			}
			_ = file.Close()
			arguments, _ := request.Params["arguments"].(map[string]any)
			text, _ := arguments["text"].(string)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo:" + text}}}
		default:
			result = map[string]any{}
		}
		if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(5)
		}
		if request.Method == "tools/call" {
			if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/claude/channel", "params": map[string]any{"content": "after-call", "meta": map[string]any{"event_type": "message"}}}); err != nil {
				os.Exit(6)
			}
		}
	}
	os.Exit(0)
}

func nativeConfig(t *testing.T) native.Config {
	t.Helper()
	return native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: "native-test-device", WorkingDirectory: t.TempDir(), Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}, "agent-two": {execprotocol.Files}}, Concurrency: 1}
}

func openNative(t *testing.T, config native.Config) *native.Engine {
	t.Helper()
	engine, err := native.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	return engine
}

func nativeRequest(t *testing.T, id, kind string, arguments any) execprotocol.Request {
	t.Helper()
	data, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return execprotocol.Request{Version: execprotocol.Version, ID: id, AgentID: "agent-one", Kind: kind, Arguments: data}
}

func nativeEventually(t *testing.T, engine *native.Engine, id string, ready func(execprotocol.Snapshot) bool) execprotocol.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := engine.Snapshot("agent-one", id, 0, 256<<10)
		if err != nil {
			t.Fatal(err)
		}
		if ready(snapshot) {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, err := engine.Snapshot("agent-one", id, 0, 256<<10)
	t.Fatal("native operation did not reach expected state", snapshot, err)
	return execprotocol.Snapshot{}
}

func nativeRun(t *testing.T, engine *native.Engine, request execprotocol.Request) execprotocol.Snapshot {
	t.Helper()
	if _, err := engine.Submit(request); err != nil {
		t.Fatal(err)
	}
	return nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() })
}

func TestNativeExecutorFilesIdentityAndRestart(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	request := nativeRequest(t, "write-note", "write", native.FileArguments{Path: "note.txt", Content: "alpha beta\n"})
	if result := nativeRun(t, engine, request); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	if result := nativeRun(t, engine, nativeRequest(t, "edit-note", "edit", native.FileArguments{Path: "note.txt", OldText: "beta", NewText: "gamma"})); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	if result := nativeRun(t, engine, nativeRequest(t, "read-note", "read", native.FileArguments{Path: "note.txt"})); result.Text() != "alpha gamma\n" {
		t.Fatal(result)
	}
	if result := nativeRun(t, engine, nativeRequest(t, "search-note", "grep", native.FileArguments{Path: ".", Pattern: "gamma"})); !strings.Contains(result.Text(), "alpha gamma") {
		t.Fatal(result)
	}
	if result := nativeRun(t, engine, nativeRequest(t, "glob-note", "glob", native.FileArguments{Path: ".", Pattern: "*.txt"})); !strings.Contains(result.Text(), "note.txt") {
		t.Fatal(result)
	}
	if _, err := engine.Snapshot("agent-two", request.ID, 0, 1024); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("cross-Agent operation exposed", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine = openNative(t, config)
	if result := nativeRun(t, engine, request); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "note.txt"))
	if err != nil || string(data) != "alpha gamma\n" {
		t.Fatal("replayed write overwrote edited file", string(data), err)
	}
	request.Arguments = json.RawMessage(`{"path":"note.txt","content":"changed"}`)
	if _, err := engine.Submit(request); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("operation identity reused", err)
	}
}

func TestNativeExecutorPTYStdinCursorAndCancellation(t *testing.T) {
	engine := openNative(t, nativeConfig(t))
	request := nativeRequest(t, "interactive", "exec_command", native.CommandArguments{Command: "printf 'ready\\n'; read answer; printf 'answer=%s\\n' \"$answer\"", TTY: true})
	if _, err := engine.Submit(request); err != nil {
		t.Fatal(err)
	}
	ready := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return strings.Contains(snapshot.Text(), "ready") })
	input := nativeRequest(t, "stdin-once", "write_stdin", native.StdinArguments{OperationID: request.ID, Chars: "hello\n", After: ready.NextCursor, YieldTimeMS: 100})
	if result := nativeRun(t, engine, input); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	complete := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() })
	if complete.State != execprotocol.Completed || !strings.Contains(complete.Text(), "answer=hello") {
		t.Fatal(complete)
	}
	continuation, err := engine.Snapshot("agent-one", request.ID, ready.NextCursor, 64<<10)
	if err != nil || strings.Contains(continuation.Text(), "ready") || continuation.NextCursor != complete.OutputBytes {
		t.Fatal(continuation, err)
	}
	if result := nativeRun(t, engine, input); result.State != execprotocol.Completed {
		t.Fatal("stdin receipt was not retained", result)
	}
	long := nativeRequest(t, "long-command", "exec_command", native.CommandArguments{Command: "printf 'started\\n'; sleep 120"})
	if _, err := engine.Submit(long); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, long.ID, func(snapshot execprotocol.Snapshot) bool { return strings.Contains(snapshot.Text(), "started") })
	if err := engine.Cancel("agent-one", long.ID); err != nil {
		t.Fatal(err)
	}
	if result := nativeEventually(t, engine, long.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() }); result.State != execprotocol.Cancelled {
		t.Fatal(result)
	}
}

func TestNativeExecutorResultQuotaAndExplicitRetention(t *testing.T) {
	config := nativeConfig(t)
	config.OutputLimit = 8192
	config.StorageLimit = 20000
	engine := openNative(t, config)
	request := nativeRequest(t, "large-output", "exec_command", native.CommandArguments{Command: "head -c 32768 /dev/zero"})
	result := nativeRun(t, engine, request)
	if result.State != execprotocol.Completed || !result.Truncated || result.OutputBytes != config.OutputLimit {
		t.Fatal(result.State, result.Truncated, result.OutputBytes)
	}
	second := nativeRequest(t, "next", "exec_command", native.CommandArguments{Command: "printf next"})
	if _, err := engine.Submit(second); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal("unacknowledged output did not reserve quota", err)
	}
	if err := engine.Prune(time.Now().Add(30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if retained, err := engine.Snapshot("agent-one", request.ID, 0, 1); err != nil || retained.OutputExpired {
		t.Fatal("unacknowledged result rotated", retained, err)
	}
	if err := engine.Acknowledge("agent-one", request.ID, 1); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("partial output acknowledged", err)
	}
	if err := engine.Acknowledge("agent-one", request.ID, result.OutputBytes); err != nil {
		t.Fatal(err)
	}
	if err := engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if result := nativeRun(t, engine, second); result.Text() != "next" {
		t.Fatal(result)
	}
	if replay, err := engine.Submit(request); err != nil || !replay.OutputExpired || replay.State != execprotocol.Completed {
		t.Fatal("expired output caused replay", replay, err)
	}
}

func TestNativeExecutorLocalGrantAndCredentialBoundary(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	request := nativeRequest(t, "forbidden", "exec_command", native.CommandArguments{Command: "printf forbidden"})
	request.AgentID = "agent-two"
	if _, err := engine.Submit(request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal(err)
	}
	request.Version++
	if _, err := engine.Submit(request); !errors.Is(err, execprotocol.ErrVersion) {
		t.Fatal(err)
	}
	if duplicate, err := native.Open(config); err == nil {
		_ = duplicate.Close()
		t.Fatal("two executors acquired the same journal")
	}
	t.Setenv("JUEX_MASTER_KEY", "must-not-reach-child")
	t.Setenv("JUEX_DEVICE_TOKEN", "must-not-reach-child")
	result := nativeRun(t, engine, nativeRequest(t, "environment", "exec_command", native.CommandArguments{Command: "printf '%s:%s' \"${JUEX_MASTER_KEY-unset}\" \"${JUEX_DEVICE_TOKEN-unset}\""}))
	if result.Text() != "unset:unset" {
		t.Fatal(result)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	config.EnvironmentID = "different-enrollment"
	if different, err := native.Open(config); err == nil {
		_ = different.Close()
		t.Fatal("state directory reused by another enrollment")
	}
}

func TestNativeExecutorCrashNeverReplaysUnknownSideEffect(t *testing.T) {
	config := nativeConfig(t)
	command := exec.Command(os.Args[0], "-test.run=^TestNativeExecutorCrashHelper$")
	command.Env = append(os.Environ(), "JUEX_NATIVE_CRASH_STATE="+config.StateDirectory, "JUEX_NATIVE_CRASH_WORK="+config.WorkingDirectory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	engine := openNative(t, config)
	request := crashRequest(t, config.WorkingDirectory)
	result, err := engine.Submit(request)
	if err != nil || result.State != execprotocol.Unknown {
		t.Fatal(result, err)
	}
	if err := engine.Prune(time.Now().Add(30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "marker"))
	if err != nil || string(marker) != "once" {
		t.Fatal("unknown side effect replayed", string(marker), err)
	}
}

func crashRequest(t *testing.T, directory string) execprotocol.Request {
	return nativeRequest(t, "crash-operation", "exec_command", native.CommandArguments{Command: "printf once >> \"$MARKER\"; sleep 2; printf finished", Environment: map[string]string{"MARKER": filepath.Join(directory, "marker")}})
}

func TestNativeExecutorCrashHelper(t *testing.T) {
	state := os.Getenv("JUEX_NATIVE_CRASH_STATE")
	if state == "" {
		return
	}
	work := os.Getenv("JUEX_NATIVE_CRASH_WORK")
	engine, err := native.Open(native.Config{StateDirectory: state, EnvironmentID: "native-test-device", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Shell}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Submit(crashRequest(t, work)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if data, err := os.ReadFile(filepath.Join(work, "marker")); err == nil && string(data) == "once" {
			os.Exit(0)
		}
	}
	t.Fatal("child did not perform its side effect")
}
