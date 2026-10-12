//go:build linux || darwin

package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"golang.org/x/sys/unix"
)

func TestTerminalPreservesTailWhileOutputPersistenceIsBlocked(t *testing.T) {
	work := t.TempDir()
	for _, name := range []string{"start", "finish"} {
		if err := unix.Mkfifo(filepath.Join(work, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: "env", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent": {execprotocol.Shell}}}
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if engine != nil {
			_ = engine.Close()
		}
	}()
	arguments := CommandArguments{TTY: true, Command: `read -r start < start; printf 'first\n'; : > written; read -r finish < finish; printf 'tail\n'; printf once >> calls`}
	if runtime.GOOS == "linux" {
		// Linux reaps the child before the blocked write finishes. Its later
		// deadline must not turn a successful exit into cancellation.
		arguments.TimeoutMS = 1000
	}
	args, _ := json.Marshal(arguments)
	request := execprotocol.Request{Version: execprotocol.Version, ID: "pty-tail", AgentID: "agent", Kind: "exec_command", Arguments: args}
	if _, err := engine.Submit(request); err != nil {
		t.Fatal(err)
	}
	var pid, fd int
	terminalEventually(t, func() bool {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		operation := engine.operations[request.ID]
		if operation.record.PID == 0 {
			return false
		}
		pid = operation.record.PID
		raw, err := operation.stdin.(*os.File).SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		if err := raw.Control(func(value uintptr) { fd = int(value) }); err != nil {
			t.Fatal(err)
		}
		return true
	})
	// Hold the same lock as journal/fsync writes. The first chunk must leave
	// the PTY before the shell may print its tail and exit.
	func() {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		terminalReleaseFIFO(t, filepath.Join(work, "start"))
		terminalEventually(t, func() bool {
			if _, err := os.Stat(filepath.Join(work, "written")); err != nil {
				return false
			}
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(poll, 0)
			return err == nil && n == 0
		})
		terminalReleaseFIFO(t, filepath.Join(work, "finish"))
		terminalEventually(t, func() bool {
			_, err := os.Stat(filepath.Join(work, "calls"))
			return err == nil
		})
		if runtime.GOOS == "linux" {
			terminalEventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
		}
		// Exceed the former close-after-exit window while persistence remains
		// blocked; this does not depend on the machine's disk speed.
		time.Sleep(1500 * time.Millisecond)
	}()
	terminalEventually(t, func() bool {
		snapshot, err := engine.Snapshot("agent", request.ID, 0, 4096)
		return err == nil && snapshot.State.Terminal()
	})
	snapshot, err := engine.Snapshot("agent", request.ID, 0, 4096)
	if err != nil || snapshot.State != execprotocol.Completed || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 || snapshot.Truncated || snapshot.Text() != "first\r\ntail\r\n" {
		t.Fatalf("PTY lost output while persistence was blocked: state=%s output=%q error=%v", snapshot.State, snapshot.Text(), err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := engine.Submit(request)
	if err != nil || replayed.Text() != snapshot.Text() || replayed.State != execprotocol.Completed {
		t.Fatal("PTY output was not durable", replayed, err)
	}
	data, err := os.ReadFile(filepath.Join(work, "calls"))
	if err != nil || strings.TrimSpace(string(data)) != "once" {
		t.Fatal("PTY recovery repeated the command", string(data), err)
	}
}

func TestTerminalReportsOutputWriteFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "trap '' HUP; printf output; sleep 30")
	command.ConfigureTerminalContext(cmd)
	want := errors.New("output persistence failed")
	input, finish, err := startTerminal(cmd, terminalFailingWriter{err: want}, 1024, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	_ = cmd.Wait()
	if err := finish(); !errors.Is(err, want) {
		t.Fatal("terminal swallowed output failure", err)
	}
}

type terminalFailingWriter struct{ err error }

func (w terminalFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func terminalEventually(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal condition did not become ready")
}

func terminalReleaseFIFO(t *testing.T, path string) {
	t.Helper()
	var file *os.File
	terminalEventually(t, func() bool {
		var err error
		file, err = os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0600)
		if err != nil && !errors.Is(err, unix.ENXIO) {
			t.Fatal(err)
		}
		return err == nil
	})
	defer file.Close()
	if _, err := file.WriteString("continue\n"); err != nil {
		t.Fatal(err)
	}
}
