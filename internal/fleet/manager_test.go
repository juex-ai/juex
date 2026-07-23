package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
)

func TestResolveSelectorUsesExactIDOrUniqueExactName(t *testing.T) {
	entries := []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "shared"),
		registryEntry("bbbbbbbb", "shared"),
		registryEntry("cccccccc", "unique"),
	}
	if got, err := resolveSelector(entries, "aaaaaaaa"); err != nil || got.ID != "aaaaaaaa" {
		t.Fatalf("resolve id = %+v, %v", got, err)
	}
	if got, err := resolveSelector(entries, "unique"); err != nil || got.ID != "cccccccc" {
		t.Fatalf("resolve name = %+v, %v", got, err)
	}
	var ambiguous *AmbiguousSelectorError
	if _, err := resolveSelector(entries, "shared"); !errors.As(err, &ambiguous) {
		t.Fatalf("ambiguous error = %T %v", err, err)
	}
	var missing *NotFoundError
	if _, err := resolveSelector(entries, "missing"); !errors.As(err, &missing) {
		t.Fatalf("missing error = %T %v", err, err)
	}
}

func TestInspectStatusRuntimeMatrix(t *testing.T) {
	runtimeState := endpoint.Runtime{
		AgentID:       "aaaaaaaa",
		InstanceID:    "instance-one",
		PID:           42,
		Endpoint:      "tcp://127.0.0.1:43123",
		StartedAt:     time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
		BinaryVersion: "1.2.3",
	}
	tests := []struct {
		name           string
		readRuntime    func(agentstate.AgentAddress) (endpoint.Runtime, error)
		processAlive   func(int) (bool, error)
		probe          func(context.Context, endpoint.Runtime) error
		maintenanceErr error
		want           RuntimeHealth
		wantVersion    string
	}{
		{
			name:        "missing runtime and free endpoint guard",
			readRuntime: func(agentstate.AgentAddress) (endpoint.Runtime, error) { return endpoint.Runtime{}, os.ErrNotExist },
			want:        RuntimeStopped,
		},
		{
			name:           "missing runtime while endpoint guard is busy",
			readRuntime:    func(agentstate.AgentAddress) (endpoint.Runtime, error) { return endpoint.Runtime{}, os.ErrNotExist },
			maintenanceErr: &endpoint.AgentAlreadyRunningError{StateDir: "agent"},
			want:           RuntimeAmbiguous,
		},
		{
			name:        "matching live runtime",
			readRuntime: func(agentstate.AgentAddress) (endpoint.Runtime, error) { return runtimeState, nil },
			processAlive: func(int) (bool, error) {
				return true, nil
			},
			probe:       func(context.Context, endpoint.Runtime) error { return nil },
			want:        RuntimeHealthy,
			wantVersion: "1.2.3",
		},
		{
			name:        "confirmed stale runtime",
			readRuntime: func(agentstate.AgentAddress) (endpoint.Runtime, error) { return runtimeState, nil },
			processAlive: func(int) (bool, error) {
				return false, nil
			},
			probe:       func(context.Context, endpoint.Runtime) error { return errors.New("connection refused") },
			want:        RuntimeUnhealthy,
			wantVersion: "1.2.3",
		},
		{
			name:        "live pid with mismatched endpoint identity",
			readRuntime: func(agentstate.AgentAddress) (endpoint.Runtime, error) { return runtimeState, nil },
			processAlive: func(int) (bool, error) {
				return true, nil
			},
			probe: func(context.Context, endpoint.Runtime) error {
				return &endpoint.IdentityMismatchError{
					Expected: runtimeState,
					Actual:   endpoint.Runtime{AgentID: "aaaaaaaa", InstanceID: "other"},
				}
			},
			want:        RuntimeAmbiguous,
			wantVersion: "1.2.3",
		},
		{
			name: "malformed runtime",
			readRuntime: func(agentstate.AgentAddress) (endpoint.Runtime, error) {
				return endpoint.Runtime{}, errors.New("bad json")
			},
			want: RuntimeAmbiguous,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDependencies()
			deps.readRuntime = test.readRuntime
			if test.processAlive != nil {
				deps.processAlive = test.processAlive
			}
			if test.probe != nil {
				deps.probe = test.probe
			}
			deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
				if test.maintenanceErr != nil {
					return nil, test.maintenanceErr
				}
				return noopGuard{}, nil
			}
			manager := &Manager{homeDir: t.TempDir(), probeTimeout: time.Second, deps: deps}
			status := manager.inspectStatus(context.Background(), registryEntry("aaaaaaaa", "agent"))
			if status.RuntimeHealth != test.want {
				t.Fatalf("runtime health = %s, want %s; status=%+v", status.RuntimeHealth, test.want, status)
			}
			if status.BinaryVersion != test.wantVersion {
				t.Fatalf("binary version = %q, want %q", status.BinaryVersion, test.wantVersion)
			}
		})
	}
}

func TestStartRetriesTransientRuntimeReadErrors(t *testing.T) {
	entry := registryEntry("aaaaaaaa", "agent")
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	var reads atomic.Int32
	deps := defaultDependencies()
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		switch reads.Add(1) {
		case 1:
			return endpoint.Runtime{}, os.ErrNotExist
		case 2:
			return endpoint.Runtime{}, &os.PathError{
				Op:   "open",
				Path: "runtime.json",
				Err:  errors.New("sharing violation"),
			}
		default:
			return runtimeState, nil
		}
	}
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.spawn = func(string, string, agentstate.RegistryEntry) (spawnedProcess, error) {
		return spawnedProcess{
			PID:     runtimeState.PID,
			Done:    make(chan error),
			LogPath: "fleet.log",
		}, nil
	}
	manager := &Manager{
		startTimeout: time.Second,
		probeTimeout: time.Second,
		deps:         deps,
	}

	status, err := manager.startEntry(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if status.RuntimeHealth != RuntimeHealthy {
		t.Fatalf("status = %+v, want healthy", status)
	}
	if got := reads.Load(); got < 3 {
		t.Fatalf("runtime reads = %d, want retry after transient error", got)
	}
}

func TestStopNeverRequestsShutdownForMismatchedIdentity(t *testing.T) {
	entry := registryEntry("aaaaaaaa", "agent")
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) { return runtimeState, nil }
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.probe = func(context.Context, endpoint.Runtime) error {
		return &endpoint.IdentityMismatchError{
			Expected: runtimeState,
			Actual:   endpoint.Runtime{AgentID: entry.ID, InstanceID: "other"},
		}
	}
	shutdownRequests := 0
	deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
		shutdownRequests++
		return nil
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		deps:         deps,
	}

	if _, err := manager.Stop(context.Background(), entry.ID); err == nil {
		t.Fatal("Stop accepted mismatched runtime identity")
	}
	if shutdownRequests != 0 {
		t.Fatalf("shutdown requests = %d, want 0", shutdownRequests)
	}
}

func TestStopRequestsExactIdentityAndWaitsForExit(t *testing.T) {
	entry := registryEntry("aaaaaaaa", "agent")
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	var stopped atomic.Bool
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		if stopped.Load() {
			return endpoint.Runtime{}, os.ErrNotExist
		}
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return !stopped.Load(), nil }
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.requestShutdown = func(_ context.Context, got endpoint.Runtime) error {
		if !got.Matches(runtimeState) {
			t.Fatalf("shutdown runtime = %+v, want %+v", got, runtimeState)
		}
		stopped.Store(true)
		return nil
	}
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		deps:         deps,
	}

	status, err := manager.Stop(context.Background(), entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.RuntimeHealth != RuntimeStopped {
		t.Fatalf("status = %+v, want stopped", status)
	}
}

func TestStartAndRestartRejectHealthyAgentsThatCannotBeStarted(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		binding agentstate.BindingKind
	}{
		{name: "disabled", enabled: false, binding: agentstate.WorkspaceBound},
		{name: "orphaned", enabled: true, binding: agentstate.WorkspaceOrphaned},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := registryEntry("aaaaaaaa", "agent")
			entry.Agent.Enabled = test.enabled
			runtimeState := endpoint.Runtime{
				AgentID:    entry.ID,
				InstanceID: "instance-one",
				PID:        42,
				Endpoint:   "tcp://127.0.0.1:43123",
				StartedAt:  time.Now().UTC(),
			}
			deps := defaultDependencies()
			deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
				return []agentstate.RegistryEntry{entry}, nil
			}
			deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
				return agentstate.WorkspaceBinding{Kind: test.binding, Reason: "test binding"}
			}
			deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) { return runtimeState, nil }
			deps.processAlive = func(int) (bool, error) { return true, nil }
			deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
			shutdownRequests := 0
			deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
				shutdownRequests++
				return errors.New("shutdown must not be requested")
			}
			spawns := 0
			deps.spawn = func(string, string, agentstate.RegistryEntry) (spawnedProcess, error) {
				spawns++
				return spawnedProcess{}, errors.New("spawn must not be called")
			}
			manager := &Manager{
				homeDir:      t.TempDir(),
				probeTimeout: time.Second,
				stopTimeout:  time.Second,
				deps:         deps,
			}

			if _, err := manager.Start(context.Background(), entry.ID); err == nil {
				t.Fatal("Start accepted an agent that cannot be started")
			}
			if _, err := manager.Restart(context.Background(), entry.ID); err == nil {
				t.Fatal("Restart accepted an agent that cannot be started")
			}
			if shutdownRequests != 0 || spawns != 0 {
				t.Fatalf("shutdown requests = %d, spawns = %d; want zero", shutdownRequests, spawns)
			}
		})
	}
}

func TestServeHoldsOneSupervisorLockAndDoesNotStopAgentsOnCancel(t *testing.T) {
	home := t.TempDir()
	first := &Manager{homeDir: home, deps: defaultDependencies()}
	first.deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{}, nil
	}
	second := &Manager{homeDir: home, deps: first.deps}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.Serve(ctx, func(action Action) {
			if action.Kind == "ready" {
				close(ready)
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("first supervisor did not become ready")
	}

	err := second.Serve(context.Background(), nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second Serve error = %T %v, want ConflictError", err, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first supervisor did not stop")
	}
}

func TestTailLogEnforcesLineAndByteBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.log")
	body := strings.Repeat("old\n", 100) + strings.Repeat("x", maxLogBytes+128) + "\nlast\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tailLog(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxLogBytes {
		t.Fatalf("tail bytes = %d, want <= %d", len(got), maxLogBytes)
	}
	if !strings.HasPrefix(string(got), truncatedLine) || !strings.HasSuffix(string(got), "last\n") {
		t.Fatalf("bounded tail missing notice or final line: prefix=%q suffix=%q", got[:min(40, len(got))], got[max(0, len(got)-40):])
	}
}

func TestLogsExplainsUnavailableFleetOwnedLog(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{name: "never created"},
		{
			name: "removed after creation",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := registryEntryAtHome(t.TempDir(), "aaaaaaaa", "adopted")
			path := fleetLogPath(entry.Address.StateDir())
			if test.prepare != nil {
				test.prepare(t, path)
			}
			manager := &Manager{homeDir: t.TempDir(), deps: defaultDependencies()}
			manager.deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
				return []agentstate.RegistryEntry{entry}, nil
			}

			_, err := manager.Logs("adopted", 20)

			var unavailable *LogUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("error = %T %v, want LogUnavailableError", err, err)
			}
			if unavailable.AgentID != entry.ID || unavailable.Path != path {
				t.Fatalf("unavailable = %+v, want agent %q path %q", unavailable, entry.ID, path)
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("error = %v, want os.ErrNotExist semantics", err)
			}
			message := err.Error()
			for _, want := range []string{
				"no fleet-owned log is available",
				"created only when fleet starts the agent",
				"started externally",
				"terminal",
				"service logs",
				"stdout/stderr redirection",
				"may have been removed",
			} {
				if !strings.Contains(message, want) {
					t.Fatalf("message = %q, want %q", message, want)
				}
			}
			for _, unwanted := range []string{path, "open ", "no such file"} {
				if strings.Contains(message, unwanted) {
					t.Fatalf("message = %q, must not contain %q", message, unwanted)
				}
			}
		})
	}
}

func TestLogsPreservesNonMissingIOErrors(t *testing.T) {
	entry := registryEntryAtHome(t.TempDir(), "aaaaaaaa", "broken-log")
	if err := os.MkdirAll(fleetLogPath(entry.Address.StateDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{homeDir: t.TempDir(), deps: defaultDependencies()}
	manager.deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}

	_, err := manager.Logs(entry.ID, 20)

	if err == nil {
		t.Fatal("Logs succeeded when fleet.log is a directory")
	}
	var unavailable *LogUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("error = %v, must not classify non-missing I/O failure as unavailable", err)
	}
}

func TestLogsRejectsInvalidRegistryEntryWithoutAddress(t *testing.T) {
	entry := agentstate.RegistryEntry{
		ID:      "invalid-slot",
		Problem: "invalid registry agent id",
	}
	manager := &Manager{homeDir: t.TempDir(), deps: defaultDependencies()}
	manager.deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}

	_, err := manager.Logs(entry.ID, 20)

	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %T %v, want ConflictError", err, err)
	}
	if conflict.AgentID != entry.ID || !strings.Contains(conflict.Reason, entry.Problem) {
		t.Fatalf("conflict = %+v, want invalid registry details", conflict)
	}
}

func registryEntry(id, name string) agentstate.RegistryEntry {
	return registryEntryAtHome(filepath.Join(os.TempDir(), "fleet-home"), id, name)
}

func registryEntryAtHome(home, id, name string) agentstate.RegistryEntry {
	workspace := filepath.Join(os.TempDir(), "fleet-test-"+id)
	address, err := agentstate.NewAgentAddress(home, id)
	if err != nil {
		panic(err)
	}
	return agentstate.RegistryEntry{
		ID:      id,
		Address: address,
		Agent: agentstate.Agent{
			ID:        id,
			Name:      name,
			Workspace: workspace,
			Enabled:   true,
			Autostart: true,
			CreatedAt: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
		},
	}
}
