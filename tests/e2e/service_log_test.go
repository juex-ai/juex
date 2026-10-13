//go:build linux || darwin

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServiceLogFailurePreservesServiceProgressAndExit(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	binary := filepath.Join(t.TempDir(), "juex-service-log")
	build := exec.Command("go", "build", "-o", binary, "./cmd/juex-service-log")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	for _, mode := range []string{"0", "7", "signal"} {
		t.Run(mode, func(t *testing.T) {
			expected, _ := strconv.Atoi(mode)
			directory := t.TempDir()
			logs := filepath.Join(directory, "logs")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "--directory", logs, "--", "sh", "-c", `umask 077; touch "$JUEX_LOG_HELPER_ROOT/ready"; while [ ! -f "$JUEX_LOG_HELPER_ROOT/release" ]; do sleep 0.01; done; dd if=/dev/zero bs=65536 count=128 2>/dev/null || exit 21; trap 'exit 0' TERM; touch "$JUEX_LOG_HELPER_ROOT/progress"; if [ "$JUEX_LOG_HELPER_MODE" = signal ]; then i=0; while [ "$i" -lt 1000 ]; do sleep 0.01; i=$((i+1)); done; exit 23; fi; exit "$JUEX_LOG_HELPER_EXIT"`)
			command.Env = append(os.Environ(), "JUEX_LOG_HELPER_ROOT="+directory, "JUEX_LOG_HELPER_EXIT="+strconv.Itoa(expected), "JUEX_LOG_HELPER_MODE="+mode, "GORACE=atexit_sleep_ms=0")
			var output bytes.Buffer
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = command.Process.Kill() })
			waitFile := func(name string) {
				t.Helper()
				for {
					if _, err := os.Stat(filepath.Join(directory, name)); err == nil {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatal("child did not reach", name)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			waitFile("ready")
			// Initialization succeeded, then the log destination becomes invalid.
			// Only this test's empty directory is replaced; service work is separate.
			if err := os.Rename(logs, logs+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(logs, []byte("unavailable log directory"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "signal" {
				waitFile("progress")
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			err := command.Wait()
			if got := command.ProcessState.ExitCode(); got != expected {
				t.Fatal("log failure changed child outcome", got, err, output.String())
			}
			waitFile("progress")
			if !strings.Contains(output.String(), "process logs:") || !strings.Contains(output.String(), logs+"/juex-") || !strings.Contains(output.String(), "not a directory") || !strings.Contains(output.String(), "bytes dropped") {
				t.Fatal("log loss not reported", output.String())
			}
		})
	}
}
