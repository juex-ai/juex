//go:build linux || darwin

package native

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"golang.org/x/sys/unix"
)

func TestHookCompletedProcessSurvivesSlowResultPersistence(t *testing.T) {
	for _, code := range []int{0, 2} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			directory := t.TempDir()
			config := Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: "hook-test", WorkingDirectory: directory, Grants: map[string][]execprotocol.Capability{"agent": {execprotocol.Shell}}}
			engine, err := Open(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if engine != nil {
					if err := engine.Close(); err != nil {
						t.Error(err)
					}
				}
			})
			releasePath := filepath.Join(directory, "release")
			if err := unix.Mkfifo(releasePath, 0600); err != nil {
				t.Fatal(err)
			}
			release, err := os.OpenFile(releasePath, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer release.Close()
			arguments, err := json.Marshal(execprotocol.HookCommand{Command: []string{"/bin/sh", "-c", `read ignored < release; printf once >> calls; printf result; exit "$1"`, "hook-test", strconv.Itoa(code)}, Input: json.RawMessage(`{}`), TimeoutMS: 1000, MaxOutputBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			request := execprotocol.Request{Version: execprotocol.Version, ID: "hook", AgentID: "agent", Kind: "run_hook", Arguments: arguments}
			if _, err := engine.Submit(request); err != nil {
				t.Fatal(err)
			}
			locked := false
			defer func() {
				if locked {
					engine.mu.Unlock()
				}
			}()
			var pid int
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				engine.mu.Lock()
				pid = engine.operations[request.ID].record.PID
				if pid != 0 {
					locked = true
					break
				}
				engine.mu.Unlock()
				time.Sleep(time.Millisecond)
			}
			if !locked {
				t.Fatal("Hook did not persist its PID")
			}
			// Hold only the output-persistence lock after the PID journal has
			// committed. The child can now finish without waiting on storage.
			if _, err := release.WriteString("finish\n"); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) && unix.Kill(pid, 0) == nil {
				time.Sleep(time.Millisecond)
			}
			if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
				t.Fatal("Hook process did not exit before the storage delay", err)
			}
			time.Sleep(1100 * time.Millisecond)
			engine.mu.Unlock()
			locked = false
			var result execprotocol.Snapshot
			deadline = time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				result, err = engine.Snapshot("agent", request.ID, 0, 4096)
				if err != nil || result.State.Terminal() {
					break
				}
				time.Sleep(time.Millisecond)
			}
			var output execprotocol.HookOutput
			if err != nil || result.State != execprotocol.Completed || result.ExitCode == nil || *result.ExitCode != code || json.Unmarshal(result.Output, &output) != nil || output.Stdout != "result" {
				t.Fatalf("completed Hook changed during persistence: state=%s exit=%v error=%q output=%q snapshot_error=%v", result.State, result.ExitCode, result.Error, result.Output, err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			engine, err = Open(config)
			if err != nil {
				t.Fatal(err)
			}
			if result, err = engine.Submit(request); err != nil || result.State != execprotocol.Completed {
				t.Fatal("completed Hook was not retained after restart", result, err)
			}
			if data, err := os.ReadFile(filepath.Join(directory, "calls")); err != nil || string(data) != "once" {
				t.Fatal("Hook was repeated", string(data), err)
			}
		})
	}
}
