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
		registryEntry("aaaaaa", "shared"),
		registryEntry("bbbbbb", "shared"),
		registryEntry("cccccc", "unique"),
	}
	if got, err := resolveSelector(entries, "aaaaaa"); err != nil || got.ID != "aaaaaa" {
		t.Fatalf("resolve id = %+v, %v", got, err)
	}
	if got, err := resolveSelector(entries, "unique"); err != nil || got.ID != "cccccc" {
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

func TestRegisteredWorkspacesReadsRegistryWithoutRuntimeInspection(t *testing.T) {
	valid := registryEntry("aaaaaa", "valid")
	invalid := registryEntry("bbbbbb", "invalid")
	invalid.Problem = "broken metadata"
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{valid, invalid}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		panic("RegisteredWorkspaces inspected runtime binding")
	}
	manager := &Manager{homeDir: t.TempDir(), deps: deps}
	got, err := manager.RegisteredWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("RegisteredWorkspaces() = %+v, want one valid Workspace", got)
	}
	if _, ok := got[filepath.Clean(valid.Agent.Workspace)]; !ok {
		t.Fatalf("RegisteredWorkspaces() = %+v, missing %q", got, valid.Agent.Workspace)
	}
}

func TestRegisteredWorkspacesCanonicalizesMovedWorkspaceSymlink(t *testing.T) {
	root := t.TempDir()
	originalWorkspace := filepath.Join(root, "original")
	movedWorkspace := filepath.Join(root, "moved")
	if err := os.MkdirAll(originalWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originalWorkspace, movedWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(movedWorkspace, originalWorkspace); err != nil {
		t.Fatal(err)
	}
	entry := registryEntry("aaaaaa", "moved")
	entry.Agent.Workspace = originalWorkspace
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}

	manager := &Manager{homeDir: t.TempDir(), deps: deps}
	got, err := manager.RegisteredWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	canonicalMoved, err := filepath.EvalSymlinks(movedWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[canonicalMoved]; !ok {
		t.Fatalf("RegisteredWorkspaces() = %+v, missing canonical %q", got, canonicalMoved)
	}
	if _, ok := got[originalWorkspace]; ok {
		t.Fatalf("RegisteredWorkspaces() retained symlink spelling %q", originalWorkspace)
	}
}

func TestInspectStatusRuntimeMatrix(t *testing.T) {
	runtimeState := endpoint.Runtime{
		AgentID:       "aaaaaa",
		InstanceID:    "instance-one",
		PID:           42,
		Endpoint:      "tcp://127.0.0.1:43123",
		StartedAt:     time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
		BinaryVersion: "1.2.3",
	}
	tests := []struct {
		name            string
		readRuntime     func(agentstate.AgentAddress) (endpoint.Runtime, error)
		processAlive    func(int) (bool, error)
		processIdentity func(int) (string, error)
		probe           func(context.Context, endpoint.Runtime) error
		maintenanceErr  error
		want            RuntimeHealth
		wantVersion     string
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
			name: "unreadable process identity with exact endpoint",
			readRuntime: func(agentstate.AgentAddress) (endpoint.Runtime, error) {
				withIdentity := runtimeState
				withIdentity.ProcessIdentity = "recorded-process"
				return withIdentity, nil
			},
			processAlive: func(int) (bool, error) { return true, nil },
			processIdentity: func(int) (string, error) {
				return "", errors.New("identity unavailable")
			},
			probe:       func(context.Context, endpoint.Runtime) error { return nil },
			want:        RuntimeAmbiguous,
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
					Actual:   endpoint.Runtime{AgentID: "aaaaaa", InstanceID: "other"},
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
			if test.processIdentity != nil {
				deps.processIdentity = test.processIdentity
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
			status := manager.inspectStatus(context.Background(), registryEntry("aaaaaa", "agent"))
			if status.RuntimeHealth != test.want {
				t.Fatalf("runtime health = %s, want %s; status=%+v", status.RuntimeHealth, test.want, status)
			}
			if status.BinaryVersion != test.wantVersion {
				t.Fatalf("binary version = %q, want %q", status.BinaryVersion, test.wantVersion)
			}
		})
	}
}

func TestStatusClassifiesReusedRuntimePIDAsStale(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	recordedProcessStart := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	runtimeState := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "instance-one",
		PID:             42,
		Endpoint:        "tcp://127.0.0.1:43123",
		StartedAt:       recordedProcessStart.Add(time.Second),
		ProcessIdentity: "recorded-process",
	}
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.processIdentity = func(int) (string, error) {
		return "reused-process", nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error {
		return &endpoint.IdentityMismatchError{
			Expected: runtimeState,
			Actual:   endpoint.Runtime{AgentID: entry.ID, InstanceID: "other"},
		}
	}
	manager := &Manager{homeDir: t.TempDir(), probeTimeout: time.Second, deps: deps}

	statuses, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	status := statuses[0]
	if status.RuntimeHealth != RuntimeUnhealthy {
		t.Fatalf("runtime health = %s, want %s; status=%+v", status.RuntimeHealth, RuntimeUnhealthy, status)
	}
	if !status.ProcessAlive || status.EndpointMatched {
		t.Fatalf("status = %+v, want live reused PID without exact endpoint identity", status)
	}
}

func TestStartCleansReusedPIDRuntimeBeforeSpawning(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	recordedProcessStart := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	runtimeState := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "instance-one",
		PID:             42,
		Endpoint:        "tcp://127.0.0.1:43123",
		StartedAt:       recordedProcessStart.Add(time.Second),
		ProcessIdentity: "recorded-process",
	}
	deps := defaultDependencies()
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.processIdentity = func(int) (string, error) {
		return "reused-process", nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error {
		return &endpoint.IdentityMismatchError{
			Expected: runtimeState,
			Actual:   endpoint.Runtime{AgentID: entry.ID, InstanceID: "other"},
		}
	}
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	removed := false
	shutdownRequests := 0
	deps.removeRuntime = func(_ agentstate.AgentAddress, got endpoint.Runtime) error {
		if !got.Matches(runtimeState) {
			t.Fatalf("removed runtime = %+v, want %+v", got, runtimeState)
		}
		removed = true
		return nil
	}
	deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
		shutdownRequests++
		return nil
	}
	spawnErr := errors.New("spawn reached")
	deps.spawn = func(string, string, agentstate.RegistryEntry) (spawnedProcess, error) {
		return spawnedProcess{}, spawnErr
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		deps:         deps,
	}

	_, err := manager.startEntry(context.Background(), entry)
	if !errors.Is(err, spawnErr) {
		t.Fatalf("start error = %v, want %v", err, spawnErr)
	}
	if !removed {
		t.Fatal("reused-PID runtime metadata was not removed before spawn")
	}
	if shutdownRequests != 0 {
		t.Fatalf("shutdown requests = %d, want zero for reused PID", shutdownRequests)
	}
}

func TestCleanupReusedPIDRequiresNonExactEndpoint(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	recordedProcessStart := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	runtimeState := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "instance-one",
		PID:             42,
		Endpoint:        "tcp://127.0.0.1:43123",
		StartedAt:       recordedProcessStart.Add(time.Second),
		ProcessIdentity: "recorded-process",
	}
	deps := defaultDependencies()
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.processIdentity = func(int) (string, error) {
		return "reused-process", nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	removed := false
	deps.removeRuntime = func(agentstate.AgentAddress, endpoint.Runtime) error {
		removed = true
		return nil
	}
	manager := &Manager{probeTimeout: time.Second, deps: deps}

	if err := manager.cleanStaleRuntime(context.Background(), entry, runtimeState); err == nil {
		t.Fatal("cleanup accepted conflicting exact endpoint evidence")
	}
	if removed {
		t.Fatal("runtime metadata was removed despite exact endpoint identity")
	}
}

func TestStatusKeepsConflictingExactEndpointEvidenceAmbiguous(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	recordedProcessStart := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	runtimeState := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "instance-one",
		PID:             42,
		Endpoint:        "tcp://127.0.0.1:43123",
		StartedAt:       recordedProcessStart.Add(time.Second),
		ProcessIdentity: "recorded-process",
	}
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.processIdentity = func(int) (string, error) {
		return "reused-process", nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	manager := &Manager{homeDir: t.TempDir(), probeTimeout: time.Second, deps: deps}

	statuses, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses[0].RuntimeHealth; got != RuntimeAmbiguous {
		t.Fatalf("runtime health = %s, want %s; status=%+v", got, RuntimeAmbiguous, statuses[0])
	}
}

func TestStartRetriesTransientRuntimeReadErrors(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
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
	entry := registryEntry("aaaaaa", "agent")
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
	entry := registryEntry("aaaaaa", "agent")
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

func TestStopCompletesWhenPIDIsReusedAfterShutdown(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	recordedProcessStart := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	runtimeState := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "instance-one",
		PID:             42,
		Endpoint:        "tcp://127.0.0.1:43123",
		StartedAt:       recordedProcessStart.Add(time.Second),
		ProcessIdentity: "recorded-process",
	}
	var shutdownRequested atomic.Bool
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		if shutdownRequested.Load() {
			return endpoint.Runtime{}, os.ErrNotExist
		}
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.processIdentity = func(int) (string, error) {
		if shutdownRequested.Load() {
			return "reused-process", nil
		}
		return "recorded-process", nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
		shutdownRequested.Store(true)
		return nil
	}
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		stopTimeout:  200 * time.Millisecond,
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
			entry := registryEntry("aaaaaa", "agent")
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

func TestServeRecoversReusedPIDRuntimeAndAutostartsAgent(t *testing.T) {
	home := t.TempDir()
	entry := registryEntryAtHome(home, "aaaaaa", "agent")
	recordedProcessStart := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	staleRuntime := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "stale-instance",
		PID:             42,
		Endpoint:        "tcp://127.0.0.1:43123",
		StartedAt:       recordedProcessStart.Add(time.Second),
		ProcessIdentity: "stale-process",
	}
	newProcessStart := recordedProcessStart.Add(2 * time.Minute)
	newRuntime := endpoint.Runtime{
		AgentID:         entry.ID,
		InstanceID:      "new-instance",
		PID:             84,
		Endpoint:        "tcp://127.0.0.1:43124",
		StartedAt:       newProcessStart.Add(time.Second),
		ProcessIdentity: "new-process",
	}
	var removed atomic.Bool
	var spawned atomic.Bool
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		switch {
		case spawned.Load():
			return newRuntime, nil
		case removed.Load():
			return endpoint.Runtime{}, os.ErrNotExist
		default:
			return staleRuntime, nil
		}
	}
	deps.processAlive = func(pid int) (bool, error) { return pid == 42 || pid == 84, nil }
	deps.processIdentity = func(pid int) (string, error) {
		if pid == 84 {
			return "new-process", nil
		}
		return "reused-process", nil
	}
	deps.probe = func(_ context.Context, got endpoint.Runtime) error {
		if got.Matches(newRuntime) {
			return nil
		}
		return &endpoint.IdentityMismatchError{
			Expected: got,
			Actual:   endpoint.Runtime{AgentID: entry.ID, InstanceID: "other"},
		}
	}
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	deps.removeRuntime = func(agentstate.AgentAddress, endpoint.Runtime) error {
		removed.Store(true)
		return nil
	}
	deps.spawn = func(string, string, agentstate.RegistryEntry) (spawnedProcess, error) {
		spawned.Store(true)
		return spawnedProcess{PID: newRuntime.PID, Done: make(chan error), LogPath: "fleet.log"}, nil
	}
	manager := &Manager{
		homeDir:      home,
		startTimeout: time.Second,
		probeTimeout: time.Second,
		deps:         deps,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	var actions []Action
	go func() {
		done <- manager.Serve(ctx, func(action Action) {
			actions = append(actions, action)
			if action.Kind == "ready" {
				close(ready)
			}
		})
	}()
	select {
	case <-ready:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("supervisor did not finish startup reconciliation")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !removed.Load() || !spawned.Load() {
		t.Fatalf("removed = %v, spawned = %v", removed.Load(), spawned.Load())
	}
	wantKinds := []string{"cleaned", "started", "ready"}
	for _, want := range wantKinds {
		found := false
		for _, action := range actions {
			if action.Kind == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("actions = %+v, missing %q", actions, want)
		}
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
			entry := registryEntryAtHome(t.TempDir(), "aaaaaa", "adopted")
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
	entry := registryEntryAtHome(t.TempDir(), "aaaaaa", "broken-log")
	sentinel := &os.PathError{
		Op:   "open",
		Path: fleetLogPath(entry.Address.StateDir()),
		Err:  os.ErrPermission,
	}
	manager := &Manager{homeDir: t.TempDir(), deps: defaultDependencies()}
	manager.deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	manager.deps.readLog = func(string, int) ([]byte, error) {
		return nil, sentinel
	}

	_, err := manager.Logs(entry.ID, 20)

	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel %v", err, sentinel)
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
