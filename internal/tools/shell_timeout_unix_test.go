//go:build !windows

package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuiltins_ShellTimeoutKillsChildProcessGroup(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, BuiltinOptions{ToolTimeoutSeconds: 1})

	start := time.Now()
	out, info, err := r.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd": "printf 'child still owns pipe\\n'; sleep 5 & wait",
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
