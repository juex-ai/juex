package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativePrivatePATHForProcessKinds(t *testing.T) {
	for _, mode := range []string{"paired", "managed-host"} {
		t.Run(mode, func(t *testing.T) {
			config := nativeConfig(t)
			if mode == "managed-host" {
				config.HomeDirectory = t.TempDir()
			}
			privatePathProcesses(t, config, os.Geteuid())
		})
	}
}

func TestNativePrivateEnvironmentCrashAndAgentIsolation(t *testing.T) {
	t.Run("accepted-crash-never-replays", func(t *testing.T) {
		config := nativeConfig(t)
		command := exec.Command(os.Args[0], "-test.run=^TestNativeExecutorCrashHelper$")
		command.Env = append(os.Environ(), "JUEX_NATIVE_CRASH_STATE="+config.StateDirectory, "JUEX_NATIVE_CRASH_WORK="+config.WorkingDirectory, "JUEX_NATIVE_CRASH_KIND=private_environment")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
		engine := openNative(t, config)
		request := crashRequest(t, config.WorkingDirectory, "private_environment")
		result, err := engine.SubmitWithEnvironment(request, map[string]string{"PRIVATE_PROCESS_VALUE": "private-before-crash"})
		if err != nil || result.State != execprotocol.Unknown {
			t.Fatal("accepted secret-bearing work replayed", result.State, err)
		}
		if _, err := engine.SubmitWithEnvironment(request, map[string]string{"PRIVATE_PROCESS_VALUE": "after-crash"}); !errors.Is(err, execprotocol.ErrConflict) {
			t.Fatal("unknown operation adopted new secret", err)
		}
		data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "marker"))
		if err != nil || string(data) != "once" {
			t.Fatal("external side effect repeated", err)
		}
	})
	t.Run("two-agents-same-device", func(t *testing.T) {
		config := nativeConfig(t)
		config.Concurrency = 2
		config.Grants["agent-two"] = []execprotocol.Capability{execprotocol.Shell}
		engine := openNative(t, config)
		for _, entry := range []struct {
			agent string
			size  int
		}{{"agent-one", 13}, {"agent-two", 29}} {
			request := nativeRequest(t, entry.agent, "exec_command", native.CommandArguments{Command: `sleep 0.05; printf '%s' "$PRIVATE_PROCESS_VALUE" | wc -c`})
			request.AgentID = entry.agent
			values := map[string]string{"PRIVATE_PROCESS_VALUE": strings.Repeat("x", entry.size)}
			if _, err := engine.SubmitWithEnvironment(request, values); err != nil {
				t.Fatal(err)
			}
			values["PRIVATE_PROCESS_VALUE"] = "changed-after-submit"
		}
		for _, entry := range []struct {
			agent string
			size  int
		}{{"agent-one", 13}, {"agent-two", 29}} {
			var snapshot execprotocol.Snapshot
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				var err error
				snapshot, err = engine.Snapshot(entry.agent, entry.agent, 0, 1024)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.State.Terminal() {
					break
				}
			}
			if snapshot.State != execprotocol.Completed || strings.TrimSpace(snapshot.Text()) != strconv.Itoa(entry.size) {
				t.Fatal("process environment crossed Agent or accepted mutable values", entry.agent, snapshot.State)
			}
		}
	})
}

// The same process cases run against the Hosted helper after dropping UID.
func privatePathProcesses(t *testing.T, config native.Config, uid int) {
	t.Helper()
	bin := filepath.Join(config.WorkingDirectory, "private-bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	probe := "juex-private-path-probe"
	// Exercise PATH selection without adding a newly written script's launch
	// policy to this identity test, especially on macOS under a full race suite.
	if err := os.Symlink("/usr/bin/id", filepath.Join(bin, probe)); err != nil {
		t.Fatal(err)
	}
	wrong := t.TempDir()
	if err := os.WriteFile(filepath.Join(wrong, probe), []byte("#!/bin/sh\nprintf connector-path"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrong+":"+os.Getenv("PATH"))
	helper := filepath.Join(bin, "juex-private-mcp")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec \"$TEST_HELPER_BINARY\" -test.run='^TestNativeExecutorMCPHelper$'"), 0755); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"PATH": bin + ":/usr/bin:/bin", "TEST_HELPER_BINARY": binary}
	engine := openNative(t, config)
	for _, kind := range []string{"run_hook", "observe_command"} {
		var args any = execprotocol.HookCommand{Command: []string{probe, "-u"}, Input: json.RawMessage(`{}`), TimeoutMS: 3000, MaxOutputBytes: 4096}
		if kind == "observe_command" {
			args = execprotocol.ObservableCommand{Command: []string{probe, "-u"}}
		}
		req := nativeRequest(t, kind, kind, args)
		if _, err := engine.SubmitWithEnvironment(req, values); err != nil {
			t.Fatal(err)
		}
		snapshot := nativeEventually(t, engine, req.ID, func(s execprotocol.Snapshot) bool { return s.State.Terminal() })
		output := snapshot.Text()
		if kind == "run_hook" {
			var result execprotocol.HookOutput
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.Stderr != "" || result.Overflow {
				t.Fatal("PATH probe produced unexpected hook output", result)
			}
			output = result.Stdout
		} else {
			output = ""
			for _, line := range strings.Split(strings.TrimSpace(snapshot.Text()), "\n") {
				var event struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				output += event.Text
			}
		}
		if snapshot.State != execprotocol.Completed || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 || strings.TrimSpace(output) != strconv.Itoa(uid) {
			t.Fatal(kind, snapshot)
		}
	}
	connection := nativeRequest(t, "private-mcp", "mcp_connect", native.MCPArguments{Command: "juex-private-mcp", Environment: map[string]string{"JUEX_NATIVE_MCP_HELPER": "1", "JUEX_NATIVE_MCP_COUNTER": filepath.Join(config.WorkingDirectory, "private-mcp-counter")}})
	if _, err := engine.SubmitWithEnvironment(connection, values); err != nil {
		t.Fatal(err)
	}
	connected := nativeEventually(t, engine, connection.ID, func(s execprotocol.Snapshot) bool {
		return s.State.Terminal() || strings.Contains(s.Text(), `"type":"connected"`)
	})
	if connected.State.Terminal() {
		t.Fatal(connected)
	}
	call := nativeRun(t, engine, nativeRequest(t, "private-mcp-call", "mcp_call", native.MCPArguments{ConnectionID: connection.ID, Name: "echo", Arguments: map[string]any{"text": "private-path"}}))
	if call.State != execprotocol.Completed || !strings.Contains(call.Text(), "echo:private-path") {
		t.Fatal(call)
	}
	if err := engine.Cancel("agent-one", connection.ID); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, connection.ID, func(s execprotocol.Snapshot) bool { return s.State.Terminal() })
}
