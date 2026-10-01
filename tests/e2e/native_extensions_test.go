//go:build linux || darwin

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

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
