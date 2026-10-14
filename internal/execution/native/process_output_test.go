//go:build linux || darwin

package native

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"golang.org/x/sys/unix"
)

func TestProcessPreservesDurableOutputAfterExit(t *testing.T) {
	for _, kind := range []string{"observe_command", "exec_command"} {
		for _, blocked := range []bool{false, true} {
			name := "normal"
			if blocked {
				name = "blocked_durable_sink"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
				work := t.TempDir()
				fifo := filepath.Join(work, "release")
				if err := unix.Mkfifo(fifo, 0600); err != nil {
					t.Fatal(err)
				}
				e, err := Open(Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: "env", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent": {execprotocol.Shell}}})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := e.Close(); err != nil {
						t.Error(err)
					}
				}()
				args, _ := json.Marshal(execprotocol.ObservableCommand{Command: []string{"/bin/sh", "-c", `read -r release < release; printf '{"message":"evidence"}\n'`}, Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl"}}})
				if kind == "exec_command" {
					args, _ = json.Marshal(CommandArguments{Command: `read -r release < release; printf evidence`})
				}
				req := execprotocol.Request{Version: execprotocol.Version, ID: "process-output", AgentID: "agent", Kind: kind, Arguments: args}
				if _, err := e.Submit(req); err != nil {
					t.Fatal(err)
				}
				pid := 0
				terminalEventually(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); pid = e.operations[req.ID].record.PID; return pid > 0 })
				if blocked {
					e.mu.Lock()
				}
				func() {
					if blocked {
						defer e.mu.Unlock()
					}
					terminalReleaseFIFO(t, fifo)
					terminalEventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
					if blocked {
						time.Sleep(2500 * time.Millisecond)
					}
				}()
				var snapshot execprotocol.Snapshot
				terminalEventually(t, func() bool {
					snapshot, err = e.Snapshot("agent", req.ID, 0, 64<<10)
					return err == nil && snapshot.State.Terminal()
				})
				t.Logf("child_reaped=true state=%s exit=%v error=%q output=%q", snapshot.State, snapshot.ExitCode, snapshot.Error, snapshot.Text())
				if snapshot.ExitCode == nil || *snapshot.ExitCode != 0 || !strings.Contains(snapshot.Text(), "evidence") {
					t.Fatalf("missing original output or exit: %+v", snapshot)
				}
				if snapshot.State != execprotocol.Completed || snapshot.Error != "" {
					t.Fatalf("durable sink delay changed successful child outcome (WaitDelay=%v): %s", snapshot.Error == exec.ErrWaitDelay.Error(), snapshot.Error)
				}
			})
		}
	}
}
