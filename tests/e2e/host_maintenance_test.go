//go:build postgres && native_service

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/host"
	"github.com/juex-ai/juex/internal/execution/hostservice"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"github.com/juex-ai/juex/internal/management"
)

func TestHostMaintenanceStopsUncommittedProvisioningWithoutReleasingParentLock(t *testing.T) {
	executorBinary, executionBinary := os.Getenv("JUEX_EXECUTOR_BINARY"), os.Getenv("JUEX_EXECUTION_BINARY")
	if !filepath.IsAbs(executorBinary) || !filepath.IsAbs(executionBinary) {
		t.Fatal("candidate Execution and executor binary paths are required")
	}
	f := executionDatabase(t)
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	config := managed.HostConfiguration{Backend: host.Config{Root: filepath.Join(root, "workspace"), ControlRoot: filepath.Join(root, "control"), Identity: uuid.NewString(), Executable: executorBinary, Server: server.URL, InsecureHTTP: true}}
	for _, path := range []string{config.Backend.Root, config.Backend.ControlRoot} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := host.New(config.Backend)
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Managed = &execution.ManagedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32)}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Never provisioned"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.agent.ID, other.ID} {
		if _, err := f.execution.Environments(ctx, f.actor, f.tenant, id); err != nil {
			t.Fatal(err)
		}
	}
	resources, err := f.executionStore.HostAllocations(ctx)
	if err != nil || len(resources) != 2 {
		t.Fatal(resources, err)
	}
	r := resources[0]
	if r.Provisioned {
		t.Fatal("allocation already provisioned")
	}
	// Ensure crosses the OS boundary before the enclosing database transaction
	// can commit its provisioned bit. Reproduce that interrupted transaction.
	if err := backend.Ensure(ctx, r, "isolated-uncommitted-enrollment"); err != nil {
		t.Fatal(err)
	}
	m, err := hostservice.New(filepath.Join(config.Backend.ControlRoot, r.EnvironmentID), executorBinary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Remove(ctx); err != nil {
			t.Error(err)
		}
	})
	if status, err := m.Status(); err != nil || !status.Running {
		t.Fatal(status, err)
	}
	directory := filepath.Join(root, "maintenance")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admission.lock", "draining"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "host.json")
	data, _ := json.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	address := f.pool.Config().ConnString()
	if _, err := managed.StopManagedHosts(ctx, address, path, directory, nil); err == nil || !strings.Contains(err.Error(), "other business database") {
		t.Fatal("online platform was not fenced", err)
	}
	f.pool.Close()
	lock, err := os.Open(filepath.Join(directory, "admission.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executionBinary, "stop-hosts", "--host-config", path, "--lock-fd", "3")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "JUEX_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "JUEX_DATABASE_URL="+address, "JUEX_MAINTENANCE_DIR="+directory)
	cmd.ExtraFiles = []*os.File{lock}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline stop: %v %s", err, out)
	}
	status, err := m.Status()
	if err != nil || status.Running || !status.CleanExit {
		t.Fatal("uncommitted executor remained alive", status, err)
	}
	if _, err := os.Stat(filepath.Join(m.StateDirectory, "enrollment.json")); err != nil {
		t.Fatal("stop removed enrollment", err)
	}
	gate, err := maintenance.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := gate.Control(); err == nil {
		done()
		t.Fatal("child released the parent's exclusive barrier")
	}
}
