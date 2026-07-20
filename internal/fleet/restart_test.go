package fleet

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/runtime"
)

func TestRestartAutoResumeLifecycle(t *testing.T) {
	tests := []struct {
		name           string
		state          runtime.SessionRuntimeState
		detectErr      error
		confirmState   runtime.TurnLifecycleState
		confirmErr     error
		resumeErr      error
		wantRequired   bool
		wantSent       bool
		wantDiagnostic string
	}{
		{
			name:  "idle does not resume",
			state: runtime.SessionRuntimeIdle,
		},
		{
			name:         "active turn resumes",
			state:        runtime.SessionRuntimeTurnActive,
			confirmState: runtime.TurnLifecycleCancelled,
			wantRequired: true,
			wantSent:     true,
		},
		{
			name:         "draining pending resumes",
			state:        runtime.SessionRuntimeDrainingPending,
			confirmState: runtime.TurnLifecycleCancelled,
			wantRequired: true,
			wantSent:     true,
		},
		{
			name:         "turn completed before shutdown does not resume",
			state:        runtime.SessionRuntimeTurnActive,
			confirmState: runtime.TurnLifecycleCompleted,
		},
		{
			name:           "confirmation failure does not risk duplicate continuation",
			state:          runtime.SessionRuntimeTurnActive,
			confirmErr:     errors.New("replacement status unavailable"),
			wantDiagnostic: "replacement status unavailable",
		},
		{
			name:           "detection failure preserves ordinary restart",
			detectErr:      errors.New("status route unavailable"),
			wantDiagnostic: "status route unavailable",
		},
		{
			name:           "resume failure is reported without failing restart",
			state:          runtime.SessionRuntimeTurnActive,
			confirmState:   runtime.TurnLifecycleCancelled,
			resumeErr:      errors.New("resume rejected"),
			wantRequired:   true,
			wantDiagnostic: "resume rejected",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, events := restartTestManager(t, []agentstate.RegistryEntry{
				registryEntry("aaaaaaaa", "alpha"),
			})
			activityReads := 0
			manager.deps.readRestartActivity = func(context.Context, endpoint.Runtime) (restartActivity, error) {
				activityReads++
				if activityReads == 1 {
					*events = append(*events, "detect")
					if test.detectErr != nil {
						return restartActivity{}, test.detectErr
					}
					return restartActivity{
						SessionID: "session-one",
						TurnID:    "turn-original",
						State:     test.state,
						TurnState: runtime.TurnLifecycleActive,
					}, nil
				}
				*events = append(*events, "confirm")
				if test.confirmErr != nil {
					return restartActivity{}, test.confirmErr
				}
				return restartActivity{
					SessionID: "session-one",
					TurnID:    "turn-original",
					State:     runtime.SessionRuntimeFailed,
					TurnState: test.confirmState,
				}, nil
			}
			manager.deps.postRestartResume = func(
				_ context.Context,
				_ endpoint.Runtime,
				sessionID string,
				prompt string,
			) (string, error) {
				*events = append(*events, "resume")
				if sessionID != "session-one" {
					t.Fatalf("resume session = %q", sessionID)
				}
				if !strings.Contains(prompt, "System notice") {
					t.Fatalf("resume prompt = %q", prompt)
				}
				if test.resumeErr != nil {
					return "", test.resumeErr
				}
				return "turn-resume", nil
			}

			result, err := manager.Restart(context.Background(), "aaaaaaaa")
			if err != nil {
				t.Fatalf("Restart returned error: %v", err)
			}
			if result.RuntimeHealth != RuntimeHealthy {
				t.Fatalf("restart status = %+v", result.AgentStatus)
			}
			if result.Resume.Required != test.wantRequired || result.Resume.Sent != test.wantSent {
				t.Fatalf("resume = %+v", result.Resume)
			}
			if test.wantDiagnostic == "" {
				if result.Resume.Error != "" {
					t.Fatalf("unexpected resume diagnostic %q", result.Resume.Error)
				}
			} else if !strings.Contains(result.Resume.Error, test.wantDiagnostic) {
				t.Fatalf("resume diagnostic = %q, want %q", result.Resume.Error, test.wantDiagnostic)
			}

			gotEvents := strings.Join(*events, ",")
			if !strings.HasPrefix(gotEvents, "detect,shutdown,spawn") {
				t.Fatalf("events = %q, want detect before shutdown and spawn", gotEvents)
			}
			if test.wantRequired && test.resumeErr == nil {
				if gotEvents != "detect,shutdown,spawn,confirm,resume" {
					t.Fatalf("events = %q, want confirmed resume only after spawn", gotEvents)
				}
			} else if !test.wantRequired && strings.Contains(gotEvents, "resume") {
				t.Fatalf("events = %q, unexpected resume", gotEvents)
			}
		})
	}
}

func TestStopNeverDetectsOrPostsResume(t *testing.T) {
	manager, _ := restartTestManager(t, []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "alpha"),
	})
	manager.deps.readRestartActivity = func(context.Context, endpoint.Runtime) (restartActivity, error) {
		t.Fatal("Stop called readRestartActivity")
		return restartActivity{}, nil
	}
	manager.deps.postRestartResume = func(context.Context, endpoint.Runtime, string, string) (string, error) {
		t.Fatal("Stop called postRestartResume")
		return "", nil
	}

	status, err := manager.Stop(context.Background(), "aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if status.RuntimeHealth != RuntimeStopped {
		t.Fatalf("status = %+v", status)
	}
}

func TestRestartRunningAgentsFiltersAndContinuesAfterFailure(t *testing.T) {
	entries := []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "healthy-one"),
		registryEntry("bbbbbbbb", "stopped"),
		registryEntry("cccccccc", "disabled"),
		registryEntry("dddddddd", "unbound"),
		registryEntry("eeeeeeee", "ambiguous"),
		registryEntry("ffffffff", "restart-fails"),
		registryEntry("gggggggg", "healthy-after-failure"),
	}
	entries[2].Agent.Enabled = false

	manager, events := restartTestManager(t, entries)
	readRuntime := manager.deps.readRuntime
	manager.deps.readRuntime = func(agentDir string) (endpoint.Runtime, error) {
		id := agentIDFromDir(agentDir)
		switch id {
		case "bbbbbbbb":
			return endpoint.Runtime{}, os.ErrNotExist
		case "eeeeeeee":
			return endpoint.Runtime{
				AgentID: id, InstanceID: "instance-" + id, PID: 70,
				Endpoint: "tcp://127.0.0.1:43123", StartedAt: time.Now().UTC(),
			}, nil
		default:
			return readRuntime(agentDir)
		}
	}
	manager.deps.inspectBinding = func(entry agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		if entry.ID == "dddddddd" {
			return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceOrphaned, Reason: "gone"}
		}
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	processAlive := manager.deps.processAlive
	manager.deps.processAlive = func(pid int) (bool, error) {
		if pid == 70 {
			return true, nil
		}
		return processAlive(pid)
	}
	probe := manager.deps.probe
	manager.deps.probe = func(ctx context.Context, state endpoint.Runtime) error {
		if state.AgentID == "eeeeeeee" {
			return errors.New("unverified endpoint")
		}
		return probe(ctx, state)
	}
	manager.deps.readRestartActivity = func(_ context.Context, state endpoint.Runtime) (restartActivity, error) {
		*events = append(*events, "restart:"+state.AgentID)
		if state.AgentID == "ffffffff" {
			return restartActivity{}, errors.New("old status unavailable")
		}
		return restartActivity{}, nil
	}
	requestShutdown := manager.deps.requestShutdown
	manager.deps.requestShutdown = func(ctx context.Context, state endpoint.Runtime) error {
		if state.AgentID == "ffffffff" {
			return errors.New("shutdown failed")
		}
		*events = append(*events, "shutdown:"+state.AgentID)
		return requestShutdown(ctx, state)
	}

	result, err := manager.RestartRunningAgents(context.Background())
	if err == nil {
		t.Fatal("RestartRunningAgents returned nil aggregate error")
	}
	if result.Restarted != 2 || result.Skipped != 4 || result.Failed != 1 {
		t.Fatalf("result counts = %+v", result)
	}
	gotEvents := strings.Join(*events, ",")
	for _, want := range []string{
		"restart:aaaaaaaa",
		"restart:ffffffff",
		"restart:gggggggg",
	} {
		if !strings.Contains(gotEvents, want) {
			t.Fatalf("events = %q, missing %q", gotEvents, want)
		}
	}
	for _, unwanted := range []string{
		"restart:bbbbbbbb",
		"restart:cccccccc",
		"restart:dddddddd",
		"restart:eeeeeeee",
	} {
		if strings.Contains(gotEvents, unwanted) {
			t.Fatalf("events = %q, contains skipped %q", gotEvents, unwanted)
		}
	}
}

func TestRestartRunningAgentsRechecksEligibilityUnderLifecycleLock(t *testing.T) {
	manager, events := restartTestManager(t, []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "stops-after-snapshot"),
	})
	readRuntime := manager.deps.readRuntime
	reads := 0
	manager.deps.readRuntime = func(agentDir string) (endpoint.Runtime, error) {
		reads++
		if reads > 1 {
			return endpoint.Runtime{}, os.ErrNotExist
		}
		return readRuntime(agentDir)
	}

	result, err := manager.RestartRunningAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Restarted != 0 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Items) != 1 ||
		result.Items[0].Outcome != RestartAgentSkipped ||
		!strings.Contains(result.Items[0].Reason, "stopped") {
		t.Fatalf("items = %+v", result.Items)
	}
	if len(*events) != 0 {
		t.Fatalf("events = %v, drifted agent must not restart", *events)
	}
}

func restartTestManager(
	t *testing.T,
	entries []agentstate.RegistryEntry,
) (*Manager, *[]string) {
	t.Helper()
	events := []string{}
	running := make(map[string]bool, len(entries))
	generation := make(map[string]int, len(entries))
	pidBase := make(map[string]int, len(entries))
	for index, entry := range entries {
		running[entry.ID] = true
		pidBase[entry.ID] = 100 + index*10
	}

	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return entries, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentDir string) (endpoint.Runtime, error) {
		id := agentIDFromDir(agentDir)
		if !running[id] {
			return endpoint.Runtime{}, os.ErrNotExist
		}
		return endpoint.Runtime{
			AgentID:       id,
			InstanceID:    "instance-" + id,
			PID:           pidBase[id] + generation[id],
			Endpoint:      "tcp://127.0.0.1:43123",
			StartedAt:     time.Now().UTC(),
			BinaryVersion: "test",
		}, nil
	}
	deps.acquireMaintenance = func(string) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	deps.processAlive = func(pid int) (bool, error) {
		for id, active := range running {
			if active && pid == pidBase[id]+generation[id] {
				return true, nil
			}
		}
		return false, nil
	}
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.requestShutdown = func(_ context.Context, state endpoint.Runtime) error {
		events = append(events, "shutdown")
		running[state.AgentID] = false
		return nil
	}
	deps.spawn = func(_ string, _ string, entry agentstate.RegistryEntry) (spawnedProcess, error) {
		events = append(events, "spawn")
		generation[entry.ID]++
		running[entry.ID] = true
		return spawnedProcess{
			PID:     pidBase[entry.ID] + generation[entry.ID],
			Done:    make(chan error),
			LogPath: "fleet.log",
		}, nil
	}

	return &Manager{
		homeDir:      t.TempDir(),
		executable:   "/test/juex",
		startTimeout: time.Second,
		stopTimeout:  time.Second,
		probeTimeout: time.Second,
		deps:         deps,
	}, &events
}

func agentIDFromDir(agentDir string) string {
	parts := strings.Split(strings.TrimRight(agentDir, string(os.PathSeparator)), string(os.PathSeparator))
	return parts[len(parts)-1]
}
