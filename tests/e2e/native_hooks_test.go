//go:build linux || darwin

package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeHooksBoundedArgvAndDurablePolicyResult(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	input := json.RawMessage(`{"text":"$(touch injected); \\\""}`)
	args := execprotocol.HookCommand{Command: []string{"/bin/sh", "-c", "cat > received.json; printf once >> calls; printf 'extra context'; printf 'diagnostic' >&2; exit 2"}, Input: input, TimeoutMS: 1000, MaxOutputBytes: 1024}
	request := nativeRequest(t, "hook-decision", "run_hook", args)
	for range 2 {
		result := nativeRun(t, engine, request)
		var output execprotocol.HookOutput
		if result.State != execprotocol.Completed || result.ExitCode == nil || *result.ExitCode != 2 || json.Unmarshal(result.Output, &output) != nil || output.Stdout != "extra context" || output.Stderr != "diagnostic" {
			t.Fatal(result, output)
		}
	}
	if data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "received.json")); err != nil || string(data) != string(input) {
		t.Fatal(string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "calls")); err != nil || string(data) != "once" {
		t.Fatal(string(data), err)
	}
	if _, err := os.Stat(filepath.Join(config.WorkingDirectory, "injected")); !os.IsNotExist(err) {
		t.Fatal("stdin interpreted as shell code", err)
	}
	request.ID, request.AgentID = "hook-without-shell", "agent-two"
	if _, err := engine.Submit(request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("hook bypassed shell grant", err)
	}
	args.Command = []string{"/bin/sh", "-c", "while :; do printf '1234567890'; done"}
	result := nativeRun(t, engine, nativeRequest(t, "hook-output-bound", "run_hook", args))
	var output execprotocol.HookOutput
	if result.State != execprotocol.Failed || !strings.Contains(result.Error, "output exceeded") || json.Unmarshal(result.Output, &output) != nil || !output.Overflow || len(output.Stdout) > 1024 {
		t.Fatalf("overflow state=%s error=%s output_bytes=%d overflow=%t stdout_bytes=%d", result.State, result.Error, result.OutputBytes, output.Overflow, len(output.Stdout))
	}
	args.Command, args.TimeoutMS = []string{"/bin/sh", "-c", "sleep 10"}, 20
	result = nativeRun(t, engine, nativeRequest(t, "hook-timeout", "run_hook", args))
	if result.State != execprotocol.Cancelled || !strings.Contains(result.Error, "deadline") {
		t.Fatal(result)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine = openNative(t, config)
	request.AgentID, request.ID = "agent-one", "hook-decision"
	if result := nativeRun(t, engine, request); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	if data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "calls")); err != nil || string(data) != "once" {
		t.Fatal("hook replayed on restart", string(data), err)
	}
}
