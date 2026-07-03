//go:build !windows

package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuiltins_ShellCancellationKillsChildProcessGroup(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, BuiltinOptions{ToolTimeoutSeconds: 1})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	out, info, err := r.CallWithInfo(ctx, "exec_command", map[string]any{
		"cmd":           "printf 'child still owns pipe\\n'; sleep 5 & wait",
		"yield_time_ms": 30000,
	})
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(out, "child still owns pipe") {
		t.Fatalf("cancelled output = %q, want captured stdout", out)
	}
	if info.TimedOut || info.TimeoutSeconds != 0 {
		t.Fatalf("info = %+v, want cancelled shell without generic timeout", info)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancellation waited for child process to exit: %s", elapsed)
	}
}
