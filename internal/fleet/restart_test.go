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
	"github.com/juex-ai/juex/internal/statusapi"
)

func TestRestartAutoResumeLifecycle(t *testing.T) {
	tests := []struct {
		name           string
		state          statusapi.ActivityState
		detectErr      error
		confirmState   statusapi.TurnState
		confirmKind    statusapi.StatusErrorKind
		confirmSession string
		confirmTurn    string
		confirmErr     error
		confirmWarmups int
		resumeErr      error
		wantRequired   bool
		wantSent       bool
		wantDiagnostic string
	}{
		{
			name:  "idle does not resume",
			state: statusapi.ActivityIdle,
		},
		{
			name:         "active turn resumes",
			state:        statusapi.ActivityWorking,
			confirmState: statusapi.TurnCancelled,
			confirmKind:  statusapi.StatusErrorRuntimeRestart,
			wantRequired: true,
			wantSent:     true,
		},
		{
			name:         "draining pending resumes",
			state:        statusapi.ActivityWorking,
			confirmState: statusapi.TurnCancelled,
			confirmKind:  statusapi.StatusErrorRuntimeRestart,
			wantRequired: true,
			wantSent:     true,
		},
		{
			name:           "replacement status warmup is retried",
			state:          statusapi.ActivityWorking,
			confirmState:   statusapi.TurnCancelled,
			confirmKind:    statusapi.StatusErrorRuntimeRestart,
			confirmWarmups: 1,
			wantRequired:   true,
			wantSent:       true,
		},
		{
			name:           "user stop cancellation does not resume",
			state:          statusapi.ActivityWorking,
			confirmState:   statusapi.TurnCancelled,
			confirmKind:    statusapi.StatusErrorCancelled,
			wantDiagnostic: "error kind",
		},
		{
			name:           "turn completed before shutdown does not resume",
			state:          statusapi.ActivityWorking,
			confirmState:   statusapi.TurnCompleted,
			wantDiagnostic: "turn state",
		},
		{
			name:           "different selected session does not resume",
			state:          statusapi.ActivityWorking,
			confirmState:   statusapi.TurnCancelled,
			confirmKind:    statusapi.StatusErrorRuntimeRestart,
			confirmSession: "session-other",
			wantDiagnostic: "want session",
		},
		{
			name:           "different selected turn does not resume",
			state:          statusapi.ActivityWorking,
			confirmState:   statusapi.TurnCancelled,
			confirmKind:    statusapi.StatusErrorRuntimeRestart,
			confirmTurn:    "turn-other",
			wantDiagnostic: "want session",
		},
		{
			name:           "confirmation failure does not risk duplicate continuation",
			state:          statusapi.ActivityWorking,
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
			state:          statusapi.ActivityWorking,
			confirmState:   statusapi.TurnCancelled,
			confirmKind:    statusapi.StatusErrorRuntimeRestart,
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
						TurnState: statusapi.TurnActive,
					}, nil
				}
				*events = append(*events, "confirm")
				if activityReads-1 <= test.confirmWarmups {
					return restartActivity{}, nil
				}
				if test.confirmErr != nil {
					return restartActivity{}, test.confirmErr
				}
				sessionID := test.confirmSession
				if sessionID == "" {
					sessionID = "session-one"
				}
				turnID := test.confirmTurn
				if turnID == "" {
					turnID = "turn-original"
				}
				return restartActivity{
					SessionID:     sessionID,
					TurnID:        turnID,
					State:         statusapi.ActivityIdle,
					TurnState:     test.confirmState,
					TurnErrorKind: test.confirmKind,
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
				wantEvents := []string{"detect", "shutdown", "spawn"}
				for index := 0; index <= test.confirmWarmups; index++ {
					wantEvents = append(wantEvents, "confirm")
				}
				wantEvents = append(wantEvents, "resume")
				if gotEvents != strings.Join(wantEvents, ",") {
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

func TestRestartWithoutIntentAcknowledgementDoesNotResume(t *testing.T) {
	manager, _ := restartTestManager(t, []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "alpha"),
	})
	requestRestart := manager.deps.requestRestart
	manager.deps.requestRestart = func(ctx context.Context, state endpoint.Runtime) (bool, error) {
		_, err := requestRestart(ctx, state)
		return false, err
	}
	reads := 0
	manager.deps.readRestartActivity = func(context.Context, endpoint.Runtime) (restartActivity, error) {
		reads++
		if reads == 1 {
			return restartActivity{
				SessionID: "session-one",
				TurnID:    "turn-original",
				State:     statusapi.ActivityWorking,
				TurnState: statusapi.TurnActive,
			}, nil
		}
		return restartActivity{
			SessionID:     "session-one",
			TurnID:        "turn-original",
			State:         statusapi.ActivityIdle,
			TurnState:     statusapi.TurnCancelled,
			TurnErrorKind: statusapi.StatusErrorCancelled,
		}, nil
	}
	manager.deps.postRestartResume = func(context.Context, endpoint.Runtime, string, string) (string, error) {
		t.Fatal("restart without acknowledgement posted a continuation")
		return "", nil
	}

	result, err := manager.Restart(context.Background(), "aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if result.Resume.Required || result.Resume.Sent ||
		!strings.Contains(result.Resume.Error, "not acknowledged") {
		t.Fatalf("resume = %+v", result.Resume)
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
	manager.deps.readRuntime = func(address agentstate.AgentAddress) (endpoint.Runtime, error) {
		id := address.ID()
		switch id {
		case "bbbbbbbb":
			return endpoint.Runtime{}, os.ErrNotExist
		case "eeeeeeee":
			return endpoint.Runtime{
				AgentID: id, InstanceID: "instance-" + id, PID: 70,
				Endpoint: "tcp://127.0.0.1:43123", StartedAt: time.Now().UTC(),
			}, nil
		default:
			return readRuntime(address)
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
	requestRestart := manager.deps.requestRestart
	manager.deps.requestRestart = func(ctx context.Context, state endpoint.Runtime) (bool, error) {
		if state.AgentID == "ffffffff" {
			return false, errors.New("shutdown failed")
		}
		*events = append(*events, "shutdown:"+state.AgentID)
		return requestRestart(ctx, state)
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

func TestRestartRunningAgentsReportsPostFailureStatus(t *testing.T) {
	manager, _ := restartTestManager(t, []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "spawn-fails"),
	})
	manager.deps.readRestartActivity = func(context.Context, endpoint.Runtime) (restartActivity, error) {
		return restartActivity{}, nil
	}
	manager.deps.spawn = func(string, string, agentstate.RegistryEntry) (spawnedProcess, error) {
		return spawnedProcess{}, errors.New("spawn failed")
	}

	result, err := manager.RestartRunningAgents(context.Background())
	if err == nil {
		t.Fatal("RestartRunningAgents returned nil aggregate error")
	}
	if len(result.Items) != 1 ||
		result.Items[0].Outcome != RestartAgentFailed ||
		result.Items[0].Agent.RuntimeHealth != RuntimeStopped {
		t.Fatalf("result = %+v", result)
	}
}

func TestRestartRunningAgentsRechecksEligibilityUnderLifecycleLock(t *testing.T) {
	manager, events := restartTestManager(t, []agentstate.RegistryEntry{
		registryEntry("aaaaaaaa", "stops-after-snapshot"),
	})
	readRuntime := manager.deps.readRuntime
	reads := 0
	manager.deps.readRuntime = func(address agentstate.AgentAddress) (endpoint.Runtime, error) {
		reads++
		if reads > 1 {
			return endpoint.Runtime{}, os.ErrNotExist
		}
		return readRuntime(address)
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
	deps.readRuntime = func(address agentstate.AgentAddress) (endpoint.Runtime, error) {
		id := address.ID()
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
	deps.acquireMaintenance = func(agentstate.AgentAddress) (maintenanceGuard, error) {
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
	deps.requestRestart = func(_ context.Context, state endpoint.Runtime) (bool, error) {
		events = append(events, "shutdown")
		running[state.AgentID] = false
		return true, nil
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
