//go:build linux || darwin

package native

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"golang.org/x/sys/unix"
)

func TestProcessOutputBoundsInheritedWriters(t *testing.T) {
	for _, script := range []string{`sleep 60 &`, `(while :; do printf x; sleep 0.01; done) &`} {
		t.Run(script, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
			command.ConfigureContext(cmd)
			finish, err := startProcessOutput(cmd, io.Discard, io.Discard, cancel)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Cancel() }()
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			err = finish()
			if !errors.Is(err, exec.ErrWaitDelay) {
				t.Fatalf("retained output writer did not fail: %v", err)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("descendant prevented bounded drain")
			}
		})
	}
}

type gatedProcessOutput struct {
	buffer           bytes.Buffer
	entered, release chan struct{}
	fail             error
}

func (w *gatedProcessOutput) Write(p []byte) (int, error) {
	if w.entered != nil {
		close(w.entered)
		w.entered = nil
		<-w.release
	}
	if w.fail != nil {
		return 0, w.fail
	}
	return w.buffer.Write(p)
}

func TestProcessOutputDrainsBufferedTailAfterSlowReceiver(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "complete"
		if fail {
			name = "original_receiver_error"
		}
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			fifo := filepath.Join(work, "finish")
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			sink := &gatedProcessOutput{entered: entered, release: release}
			if fail {
				sink.fail = errors.New("durable sink failure")
			}
			var stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf prefix; read -r finish < finish; printf tail; printf warning >&2`)
			cmd.Dir = work
			command.ConfigureContext(cmd)
			finish, err := startProcessOutput(cmd, sink, &stderr, cancel)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Cancel() }()
			select {
			case <-entered:
			case <-ctx.Done():
				close(release)
				t.Fatal("first output missing")
			}
			terminalReleaseFIFO(t, fifo)
			if err := cmd.Wait(); err != nil {
				close(release)
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- finish() }()
			select {
			case err := <-done:
				close(release)
				t.Fatalf("durable write abandoned: %v", err)
			case <-time.After(2500 * time.Millisecond):
			}
			close(release)
			err = <-done
			if !errors.Is(err, sink.fail) || errors.Is(err, exec.ErrWaitDelay) {
				t.Fatalf("output error: got %v want %v", err, sink.fail)
			}
			if stderr.String() != "warning" {
				t.Fatalf("stderr lost: %q", stderr.String())
			}
			if !fail && sink.buffer.String() != "prefixtail" {
				t.Fatalf("buffered tail lost: %q", sink.buffer.String())
			}
		})
	}
}

func TestProcessOutputCancellationAndStartFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 60")
	command.ConfigureContext(cmd)
	finish, err := startProcessOutput(cmd, io.Discard, io.Discard, cancel)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled child succeeded")
	}
	if err := finish(); err != nil {
		t.Fatalf("cancelled pipe drain: %v", err)
	}
	cmd = exec.Command(filepath.Join(t.TempDir(), "missing"))
	if finish, err := startProcessOutput(cmd, io.Discard, io.Discard, func() {}); !errors.Is(err, os.ErrNotExist) || finish != nil {
		t.Fatalf("start failure: %v", err)
	}
}
