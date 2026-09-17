package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/fleethttp"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleet/services"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

// This test binary is a real independent service fixture using the same App
// lease and typed control protocol that Memory will compose.
func TestFleetServiceProcess(t *testing.T) {
	instance := os.Getenv("JUEX_SERVICE_INSTANCE")
	if instance == "" {
		return
	}
	home := os.Getenv("JUEX_HOME")
	id := os.Getenv("JUEX_SERVICE_ID")
	if delay := os.Getenv("JUEX_SERVICE_TEST_DELAY"); delay != "" {
		duration, _ := time.ParseDuration(delay)
		time.Sleep(duration)
	}
	lease, err := services.Acquire(home, id, instance)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	listener, err := lease.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	stop := make(chan struct{})
	server := serviceendpoint.ControlServer(listener, lease.Identity(), func() { close(stop) })
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	if _, err := lease.Ready(listener); err != nil {
		t.Fatal(err)
	}
	fmt.Println("service fixture ready")
	select {
	case <-stop:
	case err := <-done:
		t.Fatalf("server exit: %v", err)
	case <-time.After(2 * time.Minute):
		t.Fatal("fixture watchdog")
	}
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func fixtureManager(t *testing.T, home, network string) *services.Manager {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := services.New(services.Options{Home: home, Definitions: map[string]services.Definition{"memory": {Mode: services.Managed, Enabled: true, Network: network, Command: []string{executable, "-test.run=^TestFleetServiceProcess$"}}}, StartTimeout: 5 * time.Second, StopTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if _, err := manager.Stop(ctx, "memory"); err != nil {
			t.Logf("fixture cleanup: %v", err)
		}
	})
	return manager
}

func TestFleetIndependentServicesLifecycleAndRecovery(t *testing.T) {
	networks := []string{"tcp"}
	if runtime.GOOS != "windows" {
		networks = append(networks, "unix")
	}
	for _, network := range networks {
		t.Run(network, func(t *testing.T) {
			home := t.TempDir()
			manager := fixtureManager(t, home, network)
			ctx := context.Background()
			started, err := manager.Start(ctx, "memory")
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if started.Phase != "ready" || started.Runtime == nil {
				t.Fatalf("start: %+v", started)
			}
			first := *started.Runtime
			// A missing public record models manager failure before publication; the
			// service-owned candidate is sufficient for exact-instance adoption.
			if err := os.Remove(serviceendpoint.RuntimePath(home, "memory")); err != nil {
				t.Fatal(err)
			}
			recovered := fixtureManager(t, home, network)
			adopted, err := recovered.Start(ctx, "memory")
			if err != nil {
				t.Fatal(err)
			}
			if adopted.Runtime == nil || adopted.Runtime.Identity != first.Identity {
				t.Fatalf("adoption: %+v", adopted)
			}
			manageCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { recovered.Serve(manageCtx); close(done) }()
			cancel()
			<-done
			if err := serviceendpoint.Probe(ctx, first); err != nil {
				t.Fatalf("manager cancellation stopped service: %v", err)
			}
			wrong := first
			wrong.InstanceID = "wrong"
			if err := serviceendpoint.Stop(ctx, wrong); err == nil {
				t.Fatal("wrong-instance stop accepted")
			}
			if err := serviceendpoint.Probe(ctx, first); err != nil {
				t.Fatal(err)
			}
			stopped, err := recovered.Stop(ctx, "memory")
			if err != nil {
				t.Fatal(err)
			}
			if stopped.Phase != "stopped" {
				t.Fatalf("stop: %+v", stopped)
			}
			if got := fixtureManager(t, home, network).Reconcile(ctx); len(got) != 1 || got[0].Phase != "stopped" {
				t.Fatalf("manual stop was undone: %+v", got)
			}
			second, err := recovered.Start(ctx, "memory")
			if err != nil {
				t.Fatal(err)
			}
			if second.Runtime == nil || second.Runtime.InstanceID == first.InstanceID {
				t.Fatalf("restart instance: %+v", second)
			}
			if _, err := serviceendpoint.Check(ctx, recovered.Resolver(), "memory"); err != nil {
				t.Fatalf("same resolver reconnect: %v", err)
			}
			another := fixtureManager(t, t.TempDir(), network)
			third, err := another.Start(ctx, "memory")
			if err != nil {
				t.Fatal(err)
			}
			if third.Runtime.FleetID == second.Runtime.FleetID || third.Runtime.Address == second.Runtime.Address {
				t.Fatal("cross-fleet endpoint reuse")
			}
			if err := serviceendpoint.Publish(home, *third.Runtime); err != nil {
				t.Fatal(err)
			}
			if _, err := serviceendpoint.Check(ctx, recovered.Resolver(), "memory"); err == nil {
				t.Fatal("cross-fleet record accepted")
			}
			// Restore the deliberately corrupted record so cleanup can verify ownership.
			if err := serviceendpoint.Publish(home, *second.Runtime); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFleetServiceAPIAndCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled CLI service fixture")
	}
	home := t.TempDir()
	manager := fixtureManager(t, home, "tcp")
	agents, err := fleet.New(fleet.Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	server, err := fleethttp.New(fleethttp.Options{Manager: agents, Services: manager})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(server.Handler())
	defer api.Close()
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/services", 200}, {"GET", "/api/services/missing", 404}, {"GET", "/api/services/memory/start", 405}, {"POST", "/api/services/memory/start", 200}, {"GET", "/api/services/memory/logs?lines=1", 200}, {"GET", "/api/services/memory/logs?lines=-1", 400}, {"POST", "/api/services/memory/stop", 200}} {
		request, _ := http.NewRequest(tc.method, api.URL+tc.path, nil)
		response, err := api.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, response.StatusCode)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// JSON is a YAML subset and preserves Windows command path escaping.
	if err := os.WriteFile(filepath.Join(home, "services.yaml"), []byte("fleet:\n  services:\n    imported: {mode: external, enabled: false, network: tcp, address: '127.0.0.1:9'}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"imports": []map[string]string{{"source": "services.yaml"}}, "fleet": map[string]any{"services": map[string]services.Definition{"memory": {Mode: services.Managed, Enabled: true, Network: "tcp", Command: []string{executable, "-test.run=^TestFleetServiceProcess$"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "juex.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := buildJuex(t)
	stdout, stderr, listErr := runJuexHomeCommand(binary, home, "fleet", "services", "list")
	var listed []services.Status
	if listErr != nil || json.Unmarshal([]byte(stdout), &listed) != nil || len(listed) != 2 || listed[0].ID != "imported" || listed[1].ID != "memory" {
		t.Fatalf("CLI lost imported service: %s %s %v", stdout, stderr, listErr)
	}
	for _, operation := range []string{"start", "status", "restart", "logs", "stop"} {
		stdout, stderr, err := runJuexHomeCommand(binary, home, "fleet", "services", operation, "memory")
		if err != nil {
			t.Fatalf("CLI %s: %v\n%s\n%s", operation, err, stdout, stderr)
		}
	}
	active, err := manager.Start(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	supervisor := startFleetSupervisorWithArgs(t, binary, fleetWebEnvironment(home), "--addr", "0.0.0.0:0", "--unsafe-bind-any")
	t.Cleanup(func() {
		if supervisor.cmd.ProcessState == nil {
			killSupervisor(t, supervisor)
		}
	})
	_, port, err := net.SplitHostPort(waitFleetWebReady(t, supervisor))
	if err != nil {
		t.Fatal(err)
	}
	var visible services.Status
	listed = nil
	fleetWebJSON(t, &http.Client{Timeout: 5 * time.Second}, http.MethodGet,
		"http://127.0.0.1:"+port+"/api/services", "", http.StatusOK, &listed)
	if len(listed) != 2 || listed[0].ID != "imported" || listed[1].ID != "memory" {
		t.Fatalf("HTTP lost imported service: %+v", listed)
	}
	fleetWebJSON(t, &http.Client{Timeout: 5 * time.Second}, http.MethodGet,
		"http://127.0.0.1:"+port+"/api/services/memory", "", http.StatusOK, &visible)
	if visible.Runtime == nil || visible.Runtime.Identity != active.Runtime.Identity {
		t.Fatalf("rebuilt Fleet failed to adopt service: %+v", visible)
	}
	killSupervisor(t, supervisor)
	if err := serviceendpoint.Probe(context.Background(), *active.Runtime); err != nil {
		t.Fatalf("Fleet process exit stopped service: %v", err)
	}
}

func TestFleetDelayedServiceChildIsFenced(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	t.Setenv("JUEX_SERVICE_TEST_DELAY", "800ms")
	manager := fixtureManager(t, home, "tcp")
	startCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	if _, err := manager.Start(startCtx, "memory"); err == nil {
		t.Fatal("delayed startup unexpectedly ready")
	}
	cancel()
	if _, err := manager.Stop(ctx, "memory"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUEX_SERVICE_TEST_DELAY", "")
	replacement, err := manager.Start(ctx, "memory")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	current, err := serviceendpoint.Check(ctx, manager.Resolver(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	if current.Identity != replacement.Runtime.Identity {
		t.Fatalf("delayed child replaced active writer: %+v", current)
	}
}

func TestFleetExternalDisableDoesNotTerminateService(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	managed := fixtureManager(t, home, "tcp")
	status, err := managed.Start(ctx, "memory")
	if err != nil {
		t.Fatal(err)
	}
	record := *status.Runtime
	external, err := services.New(services.Options{Home: home, Definitions: map[string]services.Definition{"memory": {Mode: services.External, Enabled: false, Network: record.Network, Address: record.Address}}})
	if err != nil {
		t.Fatal(err)
	}
	got := external.Reconcile(ctx)
	if len(got) != 1 || got[0].Phase != "disabled" {
		t.Fatalf("external disable: %+v", got)
	}
	if err := serviceendpoint.Probe(ctx, record); err != nil {
		t.Fatalf("external process terminated: %v", err)
	}
}
