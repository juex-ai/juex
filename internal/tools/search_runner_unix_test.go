//go:build darwin || linux || freebsd || netbsd || openbsd

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/sandbox"
)

func TestRipgrepRunnerKeepsReadableMatchesWhenDescendantIsUnreadable(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("system rg is unavailable")
	}
	root := t.TempDir()
	readable := filepath.Join(root, "readable")
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(readable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readable, "match.txt"), []byte("needle readable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "match.txt"), []byte("needle blocked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(blocked, 0o755); err != nil {
			t.Errorf("restore blocked directory permissions: %v", err)
		}
	}()

	runner := NewRipgrepRunner(RipgrepRunnerOptions{
		RipgrepPath: rg,
		WorkDir:     root,
		Sandbox:     sandbox.DefaultPolicy(),
	})
	result, err := runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: root})
	if err != nil {
		t.Fatalf("grep readable descendants: %v", err)
	}
	output := formatGrepResult(result)
	if !strings.Contains(output, "readable/match.txt") || strings.Contains(output, "blocked/match.txt") {
		t.Fatalf("accessible grep output = %q", output)
	}
}

func TestRipgrepRunnerSearchesSymlinkedFiles(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("system rg is unavailable")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	shared := filepath.Join(parent, "shared.txt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("needle shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(root, "config.link")); err != nil {
		t.Fatal(err)
	}
	runner := NewRipgrepRunner(RipgrepRunnerOptions{
		RipgrepPath: rg,
		WorkDir:     root,
		Sandbox:     sandbox.DefaultPolicy(),
	})
	result, err := runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if output := formatGrepResult(result); !strings.Contains(output, "config.link") {
		t.Fatalf("symlinked file grep output = %q", output)
	}
}

func TestRipgrepRunnerReportsFatalExitWithoutJSONSummary(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "broken-rg.sh")
	body := `#!/bin/sh
printf '%s\n' 'unsupported invocation' >&2
exit 2
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewRipgrepRunner(RipgrepRunnerOptions{
		RipgrepPath: script,
		WorkDir:     root,
		Sandbox:     sandbox.DefaultPolicy(),
	})
	result, err := runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: root})
	if err == nil || !strings.Contains(err.Error(), "unsupported invocation") {
		t.Fatalf("result=%+v err=%v, want fatal ripgrep diagnostic", result, err)
	}
}

func TestRipgrepRunnerCancellationKillsProcessGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	script := filepath.Join(root, "fake-rg.sh")
	body := `#!/bin/sh
(while :; do sleep 1; done) &
child=$!
printf '%s\n' "$child" > "$JUEX_TEST_RG_CHILD_PID"
printf '%s\n' '{"type":"match","data":{"path":{"text":"partial.txt"},"lines":{"text":"partial match\n"},"line_number":1}}'
while :; do sleep 1; done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUEX_TEST_RG_CHILD_PID", pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result GrepResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		runner := NewRipgrepRunner(RipgrepRunnerOptions{
			RipgrepPath: script,
			WorkDir:     root,
			Sandbox:     sandbox.DefaultPolicy(),
		})
		result, err := runner.Grep(ctx, GrepRequest{Pattern: "partial", Path: root})
		done <- outcome{result: result, err: err}
	}()

	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("fake rg child pid was not recorded")
	}
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context canceled", got.err)
		}
		if len(got.result.Matches) != 1 || !strings.Contains(formatGrepResult(got.result), "partial match") {
			t.Fatalf("partial result = %+v", got.result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("grep did not return after cancellation")
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d still exists after cancellation: %s", pid, fmt.Sprint(syscall.Kill(pid, 0)))
}
