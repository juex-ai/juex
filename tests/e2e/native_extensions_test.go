//go:build linux || darwin

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeProcessOutputTerminalReceiptSurvivesRestart(t *testing.T) {
	for _, kind := range []string{"observe_command", "exec_command"} {
		t.Run(kind, func(t *testing.T) {
			config := nativeConfig(t)
			engine := openNative(t, config)
			script := `printf once >> effects; printf '{"message":"first"}\n'; printf '{"message":"tail"}'; printf '{"message":"stderr"}\n' >&2`
			var arguments any = native.CommandArguments{Command: script}
			if kind == "observe_command" {
				arguments = execprotocol.ObservableCommand{Command: []string{"/bin/sh", "-c", script}, Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl"}}}
			}
			request := nativeRequest(t, "durable-process-output", kind, arguments)
			result := nativeRun(t, engine, request)
			if result.State != execprotocol.Completed || result.ExitCode == nil || *result.ExitCode != 0 || result.Error != "" {
				t.Fatal("successful process receipt", result)
			}
			for _, text := range []string{"first", "tail", "stderr"} {
				if !strings.Contains(result.Text(), text) {
					t.Fatal("output stream or tail missing", result)
				}
			}
			if kind == "observe_command" {
				lines := strings.Split(strings.TrimSpace(result.Text()), "\n")
				if len(lines) != 3 {
					t.Fatal("wrong observation count", result)
				}
				for _, line := range lines {
					var event struct{ Stream, Text string }
					if err := json.Unmarshal([]byte(line), &event); err != nil || (event.Stream != "stdout" && event.Stream != "stderr") || !json.Valid([]byte(event.Text)) {
						t.Fatal("invalid persisted observation", line, err)
					}
				}
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			engine = openNative(t, config)
			recovered := nativeRun(t, engine, request)
			if recovered.State != result.State || recovered.Error != result.Error || recovered.Text() != result.Text() || recovered.OutputBytes != result.OutputBytes || recovered.ExitCode == nil || *recovered.ExitCode != *result.ExitCode {
				t.Fatal("recovery changed original output receipt", recovered)
			}
			if data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "effects")); err != nil || string(data) != "once" {
				t.Fatal("recovery repeated original command", string(data), err)
			}
		})
	}
}

func TestNativeObserversStreamWithoutNewlineAndLeaveExecutionSlotsFree(t *testing.T) {
	engine := openNative(t, nativeConfig(t))
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("observer-%d", i)
		request := nativeRequest(t, id, "observe_command", execprotocol.ObservableCommand{Command: []string{"/bin/sh", "-c", "printf 'alarm'; sleep 60"}, Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "text"}}})
		if _, err := engine.Submit(request); err != nil {
			t.Fatal(err)
		}
		result := nativeEventually(t, engine, id, func(s execprotocol.Snapshot) bool { return strings.Contains(s.Text(), "alarm") })
		if result.State != execprotocol.Running {
			t.Fatal("text waited for termination", result)
		}
	}
	hook := nativeRun(t, engine, nativeRequest(t, "hook-during-observers", "run_hook", execprotocol.HookCommand{Command: []string{"/bin/echo", "available"}, Input: json.RawMessage(`{}`), TimeoutMS: 1000, MaxOutputBytes: 1024}))
	if hook.State != execprotocol.Completed || !strings.Contains(hook.Text(), "available") {
		t.Fatal("observers starved ordinary slot", hook)
	}
	for i := 0; i < 4; i++ {
		if err := engine.Cancel("agent-one", fmt.Sprintf("observer-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	result := nativeRun(t, engine, nativeRequest(t, "unicode-observer", "observe_command", execprotocol.ObservableCommand{Command: []string{"/bin/sh", "-c", `printf '\344'; sleep 0.05; printf '\275\240'; sleep 0.05; printf '\345\245\275'`}}))
	if result.State != execprotocol.Completed || strings.Contains(result.Text(), "�") || !strings.Contains(result.Text(), "你") || !strings.Contains(result.Text(), "好") {
		t.Fatal("text split Unicode", result)
	}
	overflow := nativeRun(t, engine, nativeRequest(t, "observer-jsonl-overflow", "observe_command", execprotocol.ObservableCommand{Command: []string{"/bin/sh", "-c", "while :; do printf '1234567890'; done"}, Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl"}}}))
	if overflow.State != execprotocol.Failed || !strings.Contains(overflow.Error, "64 KiB") {
		t.Fatal("JSONL overflow hidden", overflow)
	}
}
