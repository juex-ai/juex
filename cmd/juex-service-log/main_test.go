package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestChildFailureRetainsSignalAndDoesNotWaitForDescendantPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux deployment wrapper")
	}
	start := time.Now()
	err := runService([]string{"sh", "-c", "sleep 2 & exit 7"}, t.TempDir(), 50*time.Millisecond)
	if exitCode(err) != 7 || time.Since(start) > time.Second {
		t.Fatal("child failure hidden by descendant pipe", err, time.Since(start))
	}
	err = runService([]string{"sh", "-c", "kill -KILL $$"}, t.TempDir(), 50*time.Millisecond)
	if exitCode(err) != 137 {
		t.Fatal("crash mistaken for graceful termination", err, exitCode(err))
	}
}

type blockedLogs struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedLogs) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return len(p), nil
}
func (b *blockedLogs) Prune() error { return nil }
func (b *blockedLogs) Close() error { return nil }

func TestSlowLogsDoNotBlockServiceProgressOrExit(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	sink := &blockedLogs{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- runLoggedService([]string{os.Args[0], "-test.run=^TestServiceLogOutputHelper$"}, sink, 50*time.Millisecond)
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not reach log sink")
	}
	select {
	case err := <-done:
		close(sink.release)
		if exitCode(err) != 7 {
			t.Fatal("lost child outcome", err)
		}
	case <-time.After(time.Second):
		close(sink.release)
		<-done
		t.Fatal("blocked log sink prevented child progress/exit")
	}
}
func TestServiceLogOutputHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[1] != "-test.run=^TestServiceLogOutputHelper$" {
		return
	}
	block := bytes.Repeat([]byte("x"), 32<<10)
	for range 256 {
		if _, err := os.Stdout.Write(block); err != nil {
			os.Exit(8)
		}
	}
	os.Exit(7)
}

func TestBlockedLogSinkStillForwardsTermination(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	progress := filepath.Join(t.TempDir(), "progress")
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServiceLogSignalHelper$")
	command.Env = append(os.Environ(), "JUEX_LOG_SIGNAL_PROGRESS="+progress, "GORACE=atexit_sleep_ms=0")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	for {
		if _, err := os.Stat(progress); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("child blocked before independent progress")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := command.Wait(); err != nil || time.Since(start) > time.Second {
		t.Fatal("termination did not reach child through blocked log sink", err, time.Since(start))
	}
}

func TestServiceLogSignalHelper(t *testing.T) {
	if os.Getenv("JUEX_LOG_SIGNAL_PROGRESS") == "" {
		return
	}
	sink := &blockedLogs{entered: make(chan struct{}), release: make(chan struct{})}
	args := []string{"sh", "-c", `trap 'exit 0' TERM; dd if=/dev/zero bs=65536 count=128 2>/dev/null || exit 21; touch "$JUEX_LOG_SIGNAL_PROGRESS"; i=0; while [ "$i" -lt 1000 ]; do sleep 0.01; i=$((i+1)); done; exit 23`}
	os.Exit(exitCode(runLoggedService(args, sink, 50*time.Millisecond)))
}
