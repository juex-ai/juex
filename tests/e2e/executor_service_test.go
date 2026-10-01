//go:build native_service

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/hostservice"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// This opt-in test creates only its own temporary enrollment and user service.
// It requires a real logged-in OS user service manager and the candidate binary.
func TestExecutorUserServiceLifecycle(t *testing.T) {
	binary := os.Getenv("JUEX_EXECUTOR_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("JUEX_EXECUTOR_BINARY must name the candidate executable")
	}
	dir := filepath.Join(t.TempDir(), "state with spaces $HOME %i")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	config := map[string]any{"server": server.URL, "insecure_http": true, "credential": "isolated-service-test", "device": execution.Device{Environment: execprotocol.Environment{ID: "service-test", WorkingDirectory: dir}, Ceiling: map[string][]execprotocol.Capability{"agent": {execprotocol.Files, execprotocol.Shell}}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "enrollment.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, binary, append([]string{"--state", dir}, args...)...).CombinedOutput()
	}
	status := func(command string) hostservice.Status {
		output, err := call(command)
		if err != nil {
			t.Fatalf("%s: %v %s", command, err, output)
		}
		var v hostservice.Status
		if err := json.Unmarshal(output, &v); err != nil {
			t.Fatalf("%s: %v", output, err)
		}
		return v
	}
	t.Cleanup(func() {
		if output, err := call("autostart", "disable"); err != nil {
			t.Errorf("disable: %v %s", err, output)
		}
		if output, err := call("stop"); err != nil {
			t.Errorf("stop: %v %s", err, output)
		}
		if runtime.GOOS == "linux" {
			// The CLI deliberately retains its definition for the next manual start.
			// The test removes its own definition after stopping the temporary service.
			configHome, err := os.UserConfigDir()
			if err != nil {
				t.Error(err)
				return
			}
			matches, err := filepath.Glob(filepath.Join(configHome, "systemd", "user", "ai.juex.executor.*.service"))
			if err != nil {
				t.Error(err)
				return
			}
			for _, path := range matches {
				data, err := os.ReadFile(path)
				if err == nil && strings.Contains(string(data), strings.ReplaceAll(strings.ReplaceAll(dir, "$", "$$"), "%", "%%")) {
					if err := os.Remove(path); err != nil {
						t.Error(err)
					}
				}
			}
			if output, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
				t.Errorf("reload: %v %s", err, output)
			}
		}
	})
	first := status("start")
	if !first.Running || !first.Background || first.Autostart || first.EnvironmentID != "service-test" {
		t.Fatal(first)
	}
	second := status("start")
	if second.PID != first.PID {
		t.Fatal("duplicate process", first, second)
	}
	if output, err := call("autostart", "enable"); err != nil {
		t.Fatalf("enable: %v %s", err, output)
	}
	if v := status("status"); !v.Autostart || v.PID != first.PID {
		t.Fatal(v)
	}
	if output, err := call("autostart", "disable"); err != nil {
		t.Fatalf("disable: %v %s", err, output)
	}
	if v := status("status"); v.Autostart || !v.Running || v.PID != first.PID {
		t.Fatal(v)
	}
	output, err := call("logs", "--tail", "10")
	if err != nil || !strings.Contains(string(output), "Device") {
		t.Fatal(string(output), err)
	}
	if output, err := call("stop"); err != nil {
		t.Fatalf("stop: %v %s", err, output)
	}
	if v := status("status"); v.Running || v.State != "stopped" {
		t.Fatal(v)
	}
	third := status("start")
	if !third.Running || third.Fingerprint == first.Fingerprint {
		t.Fatal("restart did not acquire a new process", third)
	}
}
