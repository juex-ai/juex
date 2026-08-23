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
	"github.com/juex-ai/juex/internal/statusapi"
)

func TestEndpointReturnsOnlyBoundHealthyRuntime(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	tests := []struct {
		name       string
		binding    agentstate.BindingKind
		runtimeErr error
		alive      bool
		probeErr   error
		wantOK     bool
	}{
		{name: "bound healthy", binding: agentstate.WorkspaceBound, alive: true, wantOK: true},
		{name: "orphaned healthy", binding: agentstate.WorkspaceOrphaned, alive: true},
		{name: "stopped", binding: agentstate.WorkspaceBound, runtimeErr: os.ErrNotExist},
		{name: "dead", binding: agentstate.WorkspaceBound, probeErr: errors.New("connection refused")},
		{
			name:    "mismatched",
			binding: agentstate.WorkspaceBound,
			alive:   true,
			probeErr: &endpoint.IdentityMismatchError{
				Expected: runtimeState,
				Actual:   endpoint.Runtime{AgentID: entry.ID, InstanceID: "other"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDependencies()
			deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
				return []agentstate.RegistryEntry{entry}, nil
			}
			deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
				return agentstate.WorkspaceBinding{Kind: test.binding, Reason: "test binding"}
			}
			deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
				return runtimeState, test.runtimeErr
			}
			deps.processAlive = func(int) (bool, error) { return test.alive, nil }
			deps.probe = func(context.Context, endpoint.Runtime) error { return test.probeErr }
			deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
				return noopGuard{}, nil
			}
			manager := &Manager{
				homeDir:      t.TempDir(),
				probeTimeout: time.Second,
				deps:         deps,
			}

			got, err := manager.Endpoint(context.Background(), entry.ID)
			if test.wantOK {
				if err != nil || !got.Matches(runtimeState) {
					t.Fatalf("Endpoint = %+v, %v", got, err)
				}
				return
			}
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("Endpoint error = %T %v, want ConflictError", err, err)
			}
		})
	}
}

func TestEndpointRejectsPIDReuseAfterHealthyStatusSnapshot(t *testing.T) {
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
	var processReads atomic.Int32
	deps.processIdentity = func(int) (string, error) {
		if processReads.Add(1) == 1 {
			return "recorded-process", nil
		}
		return "reused-process", nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	manager := &Manager{homeDir: t.TempDir(), probeTimeout: time.Second, deps: deps}

	_, err := manager.Endpoint(context.Background(), entry.ID)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Endpoint error = %T %v, want ConflictError", err, err)
	}
}

func TestReadOnlyStateRequiresBoundWorkspace(t *testing.T) {
	entry := registryEntry("aaaaaa", "agent")
	tests := []struct {
		name    string
		binding agentstate.BindingKind
		wantOK  bool
	}{
		{name: "bound", binding: agentstate.WorkspaceBound, wantOK: true},
		{name: "orphaned", binding: agentstate.WorkspaceOrphaned},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDependencies()
			deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
				return []agentstate.RegistryEntry{entry}, nil
			}
			deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
				return agentstate.WorkspaceBinding{Kind: test.binding, Reason: "test binding"}
			}
			manager := &Manager{homeDir: t.TempDir(), deps: deps}

			got, err := manager.ReadOnlyState(entry.ID)
			if test.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				if got.ID != entry.ID || got.Workspace != entry.Agent.Workspace || got.StateDir != entry.Address.StateDir() {
					t.Fatalf("ReadOnlyState = %+v", got)
				}
				return
			}
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("ReadOnlyState error = %T %v, want ConflictError", err, err)
			}
		})
	}
}

func TestUpdateConfigPreflightsBeforeWriting(t *testing.T) {
	home, workspace, entry := prepareFleetConfigTest(t)
	entry.Agent.Enabled = false
	old := []byte("old: unchanged\n")
	configPath := filepath.Join(workspace, ".juex", "juex.yaml")
	if err := os.WriteFile(configPath, old, 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	deps := configTestDependencies(entry, runtimeState)
	shutdowns := 0
	deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
		shutdowns++
		return nil
	}
	manager := &Manager{
		homeDir:      home,
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		startTimeout: time.Second,
		deps:         deps,
	}

	if _, _, err := manager.UpdateConfig(context.Background(), entry.ID, validFleetConfig("new-model")); err == nil {
		t.Fatal("UpdateConfig accepted a disabled agent")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) || shutdowns != 0 {
		t.Fatalf("preflight changed state: config=%q shutdowns=%d", got, shutdowns)
	}
}

func TestUpdateConfigRejectsAmbiguousRuntimeBeforeWriting(t *testing.T) {
	home, workspace, entry := prepareFleetConfigTest(t)
	old := []byte("old: unchanged\n")
	configPath := filepath.Join(workspace, ".juex", "juex.yaml")
	if err := os.WriteFile(configPath, old, 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	deps := configTestDependencies(entry, runtimeState)
	deps.probe = func(context.Context, endpoint.Runtime) error {
		return &endpoint.IdentityMismatchError{
			Expected: runtimeState,
			Actual: endpoint.Runtime{
				AgentID:    entry.ID,
				InstanceID: "other",
			},
		}
	}
	manager := &Manager{
		homeDir:      home,
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		startTimeout: time.Second,
		deps:         deps,
	}

	if _, _, err := manager.UpdateConfig(
		context.Background(),
		entry.ID,
		validFleetConfig("new-model"),
	); err == nil {
		t.Fatal("UpdateConfig accepted an ambiguous runtime")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("ambiguous preflight changed config: %q", got)
	}
}

func TestUpdateConfigUsesRestartContinuationPolicy(t *testing.T) {
	tests := []struct {
		name           string
		activity       statusapi.ActivityState
		resumeErr      error
		wantRequired   bool
		wantSent       bool
		wantResumeCall int32
		wantDiagnostic string
	}{
		{
			name:           "active turn resumes",
			activity:       statusapi.ActivityWorking,
			wantRequired:   true,
			wantSent:       true,
			wantResumeCall: 1,
		},
		{
			name:     "idle restart does not resume",
			activity: statusapi.ActivityIdle,
		},
		{
			name:           "resume failure remains non fatal",
			activity:       statusapi.ActivityWorking,
			resumeErr:      errors.New("resume rejected"),
			wantRequired:   true,
			wantResumeCall: 1,
			wantDiagnostic: "resume rejected",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home, workspace, entry := prepareFleetConfigTest(t)
			manager, newRuntime, resumeCalls := configRestartTestManager(
				t,
				home,
				entry,
				test.activity,
				test.resumeErr,
			)

			configState, restarted, err := manager.UpdateConfig(
				context.Background(),
				entry.ID,
				validFleetConfig("new-model"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !configState.Exists || !strings.Contains(configState.Content, "new-model") {
				t.Fatalf("config state = %+v", configState)
			}
			if restarted.RuntimeHealth != RuntimeHealthy || restarted.PID != newRuntime.PID {
				t.Fatalf("status = %+v", restarted.AgentStatus)
			}
			if restarted.Resume.Required != test.wantRequired ||
				restarted.Resume.Sent != test.wantSent ||
				resumeCalls.Load() != test.wantResumeCall {
				t.Fatalf("resume = %+v, calls = %d", restarted.Resume, resumeCalls.Load())
			}
			if test.wantSent && (restarted.Resume.SessionID != "session-one" ||
				restarted.Resume.TurnID != "turn-resume") {
				t.Fatalf("resume = %+v", restarted.Resume)
			}
			if !strings.Contains(restarted.Resume.Error, test.wantDiagnostic) {
				t.Fatalf("resume diagnostic = %q, want %q", restarted.Resume.Error, test.wantDiagnostic)
			}
			body, err := os.ReadFile(filepath.Join(workspace, ".juex", "juex.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != string(validFleetConfig("new-model")) {
				t.Fatalf("written config:\n%s", body)
			}
		})
	}
}

func configRestartTestManager(
	t *testing.T,
	home string,
	entry agentstate.RegistryEntry,
	activity statusapi.ActivityState,
	resumeErr error,
) (*Manager, endpoint.Runtime, *atomic.Int32) {
	t.Helper()
	oldRuntime := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	newRuntime := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-two",
		PID:        99,
		Endpoint:   "tcp://127.0.0.1:43124",
		StartedAt:  time.Now().UTC().Add(time.Second),
	}
	var state atomic.Int32
	var activityReads atomic.Int32
	var resumeCalls atomic.Int32
	deps := configTestDependencies(entry, oldRuntime)
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		switch state.Load() {
		case 0:
			return oldRuntime, nil
		case 1:
			return endpoint.Runtime{}, os.ErrNotExist
		default:
			return newRuntime, nil
		}
	}
	deps.processAlive = func(pid int) (bool, error) {
		return (state.Load() == 0 && pid == oldRuntime.PID) ||
			(state.Load() == 2 && pid == newRuntime.PID), nil
	}
	deps.requestRestart = func(_ context.Context, got endpoint.Runtime) (bool, error) {
		if !got.Matches(oldRuntime) {
			t.Fatalf("shutdown runtime = %+v", got)
		}
		state.Store(1)
		return true, nil
	}
	deps.spawn = func(string, string, agentstate.RegistryEntry) (spawnedProcess, error) {
		state.Store(2)
		return spawnedProcess{PID: newRuntime.PID, Done: make(chan error), LogPath: "fleet.log"}, nil
	}
	deps.readRestartActivity = func(_ context.Context, got endpoint.Runtime) (restartActivity, error) {
		if call := activityReads.Add(1); call == 1 {
			if !got.Matches(oldRuntime) {
				t.Fatalf("detect runtime = %+v", got)
			}
			if activity == statusapi.ActivityIdle {
				return restartActivity{State: statusapi.ActivityIdle}, nil
			}
			return restartActivity{
				SessionID: "session-one",
				TurnID:    "turn-original",
				State:     activity,
				TurnState: statusapi.TurnActive,
			}, nil
		}
		if !got.Matches(newRuntime) {
			t.Fatalf("confirm runtime = %+v", got)
		}
		return restartActivity{
			SessionID:     "session-one",
			TurnID:        "turn-original",
			State:         statusapi.ActivityIdle,
			TurnState:     statusapi.TurnCancelled,
			TurnErrorKind: statusapi.StatusErrorRuntimeRestart,
		}, nil
	}
	deps.postRestartResume = func(
		_ context.Context,
		got endpoint.Runtime,
		sessionID string,
		prompt string,
	) (string, error) {
		resumeCalls.Add(1)
		if !got.Matches(newRuntime) || sessionID != "session-one" ||
			!strings.Contains(prompt, "System notice") {
			t.Fatalf("resume runtime/session/prompt = %+v/%q/%q", got, sessionID, prompt)
		}
		if resumeErr != nil {
			return "", resumeErr
		}
		return "turn-resume", nil
	}
	return &Manager{
		homeDir:      home,
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		startTimeout: time.Second,
		deps:         deps,
	}, newRuntime, &resumeCalls
}

func prepareFleetConfigTest(t *testing.T) (string, string, agentstate.RegistryEntry) {
	t.Helper()
	homeRoot := t.TempDir()
	home := filepath.Join(homeRoot, ".juex")
	workspace := t.TempDir()
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)
	t.Setenv("JUEX_HOME", home)
	for _, key := range []string{
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
		"PROVIDER_THINKING_EFFORT",
	} {
		t.Setenv(key, "")
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := registryEntryAtHome(home, "aaaaaa", "agent")
	entry.Agent.Workspace = workspace
	entry.Agent.Autostart = false
	return home, workspace, entry
}

func configTestDependencies(entry agentstate.RegistryEntry, runtimeState endpoint.Runtime) dependencies {
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) { return runtimeState, nil }
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	return deps
}

func validFleetConfig(model string) []byte {
	return []byte(`models: [local:` + model + `]
providers:
  - id: local
    protocol: openai/chat
    base_url: http://127.0.0.1:12345
    api_key: test-key
    models:
      - id: ` + model + `
`)
}
