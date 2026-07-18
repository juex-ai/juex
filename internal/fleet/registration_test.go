package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
)

func TestAddCreatesAndIdempotentlyUpdatesAgent(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	manager := newRegistrationTestManager(t, home)
	name := "alpha"
	autostart := true

	first, err := manager.Add(context.Background(), AddOptions{
		Workspace: workspace,
		Name:      &name,
		Autostart: &autostart,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created ||
		first.Agent.Name != name ||
		!first.Agent.Enabled ||
		!first.Agent.Autostart ||
		first.Agent.Binding != BindingBound ||
		first.Agent.RuntimeHealth != RuntimeStopped {
		t.Fatalf("first add = %+v", first)
	}

	renamed := "renamed"
	second, err := manager.Add(context.Background(), AddOptions{
		Workspace: workspace,
		Name:      &renamed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Agent.ID != first.Agent.ID || second.Agent.Name != renamed {
		t.Fatalf("idempotent add = %+v, first = %+v", second, first)
	}
	entries, err := agentstate.ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != first.Agent.ID {
		t.Fatalf("registry = %+v, want one agent", entries)
	}
}

func TestAddCanonicalizesSymlinkWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("symlink workspace unavailable: %v", err)
	}
	manager := newRegistrationTestManager(t, home)
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Add(context.Background(), AddOptions{Workspace: alias})
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.Workspace != canonicalWorkspace {
		t.Fatalf("workspace = %q, want canonical %q", first.Agent.Workspace, canonicalWorkspace)
	}
	second, err := manager.Add(context.Background(), AddOptions{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Agent.ID != first.Agent.ID {
		t.Fatalf("canonical add = %+v, first = %+v", second, first)
	}
}

func TestAddRejectsRelativePathAndUnknownMarker(t *testing.T) {
	home := t.TempDir()
	manager := newRegistrationTestManager(t, home)

	_, err := manager.Add(context.Background(), AddOptions{Workspace: "relative"})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("relative path error = %T %v, want ValidationError", err, err)
	}

	workspace := t.TempDir()
	writeFleetTestJSON(
		t,
		filepath.Join(workspace, ".juex", "juex.local.json"),
		agentstate.Marker{AgentID: "aaaaaaaa"},
	)
	_, err = manager.Add(context.Background(), AddOptions{Workspace: workspace})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("unknown marker error = %T %v, want ConflictError", err, err)
	}
	entries, listErr := agentstate.ListRegistry(home)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("unknown marker minted registry entries: %+v", entries)
	}
}

func TestSetEnabledIsReversibleAndDoesNotStartOnEnable(t *testing.T) {
	home := t.TempDir()
	manager := newRegistrationTestManager(t, home)
	added, err := manager.Add(context.Background(), AddOptions{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := manager.SetEnabled(context.Background(), added.Agent.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.RuntimeHealth != RuntimeStopped {
		t.Fatalf("disabled status = %+v", disabled)
	}
	enabled, err := manager.SetEnabled(context.Background(), added.Agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.RuntimeHealth != RuntimeStopped || enabled.PID != 0 {
		t.Fatalf("enabled status = %+v, want enabled and stopped", enabled)
	}
}

func TestSetEnabledPreservesEnabledFlagWhenStopFails(t *testing.T) {
	entry := registryEntry("aaaaaaaa", "alpha")
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
	deps.readRuntime = func(string) (endpoint.Runtime, error) { return runtimeState, nil }
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
		return errors.New("injected shutdown failure")
	}
	updateCalls := 0
	deps.updateAgent = func(string, string, agentstate.AgentUpdate) (agentstate.Agent, error) {
		updateCalls++
		return entry.Agent, nil
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		deps:         deps,
	}

	if _, err := manager.SetEnabled(context.Background(), entry.ID, false); err == nil {
		t.Fatal("SetEnabled succeeded after shutdown failure")
	}
	if updateCalls != 0 {
		t.Fatalf("metadata update calls = %d, want zero", updateCalls)
	}
}

func TestRemoveRequiresConfirmationAndCleansMatchingMarker(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	manager := newRegistrationTestManager(t, home)
	name := "alpha"
	added, err := manager.Add(context.Background(), AddOptions{
		Workspace: workspace,
		Name:      &name,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(home, "agents", added.Agent.ID)
	markerPath := filepath.Join(workspace, ".juex", "juex.local.json")

	_, err = manager.Remove(context.Background(), added.Agent.ID, RemoveOptions{ConfirmName: "wrong"})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("confirmation error = %T %v, want ValidationError", err, err)
	}
	for _, path := range []string{agentDir, markerPath} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("rejected remove changed %s: %v", path, statErr)
		}
	}

	removed, err := manager.Remove(
		context.Background(),
		added.Agent.ID,
		RemoveOptions{ConfirmName: name},
	)
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != added.Agent.ID ||
		removed.Name != name ||
		removed.Workspace != added.Agent.Workspace {
		t.Fatalf("removed agent = %+v", removed)
	}
	for _, path := range []string{agentDir, markerPath} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("removed path still exists %s: %v", path, statErr)
		}
	}
}

func TestRemoveUnnamedAgentRequiresIDConfirmation(t *testing.T) {
	entry := registryEntry("aaaaaaaa", "")
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(string) (endpoint.Runtime, error) {
		return endpoint.Runtime{}, os.ErrNotExist
	}
	deps.acquireMaintenance = func(string) (maintenanceGuard, error) {
		return noopGuard{}, nil
	}
	deleteCalls := 0
	deps.deleteRegistered = func(string, string) error {
		deleteCalls++
		return nil
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		deps:         deps,
	}

	if _, err := manager.Remove(
		context.Background(),
		entry.ID,
		RemoveOptions{ConfirmName: ""},
	); err == nil {
		t.Fatal("Remove accepted an empty confirmation for an unnamed agent")
	}
	if _, err := manager.Remove(
		context.Background(),
		entry.ID,
		RemoveOptions{ConfirmName: entry.ID},
	); err != nil {
		t.Fatal(err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
}

func TestRemoveStopsThenAcquiresMaintenanceBeforeDeleting(t *testing.T) {
	entry := registryEntry("aaaaaaaa", "alpha")
	runtimeState := endpoint.Runtime{
		AgentID:    entry.ID,
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	stopped := false
	var calls []string
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return []agentstate.RegistryEntry{entry}, nil
	}
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(string) (endpoint.Runtime, error) {
		if stopped {
			return endpoint.Runtime{}, os.ErrNotExist
		}
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return !stopped, nil }
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	deps.requestShutdown = func(context.Context, endpoint.Runtime) error {
		calls = append(calls, "shutdown")
		stopped = true
		return nil
	}
	deps.acquireMaintenance = func(string) (maintenanceGuard, error) {
		calls = append(calls, "maintenance")
		return noopGuard{}, nil
	}
	deps.deleteRegistered = func(string, string) error {
		calls = append(calls, "delete")
		return nil
	}
	manager := &Manager{
		homeDir:      t.TempDir(),
		probeTimeout: time.Second,
		stopTimeout:  time.Second,
		deps:         deps,
	}

	if _, err := manager.Remove(
		context.Background(),
		entry.ID,
		RemoveOptions{SkipConfirmation: true},
	); err != nil {
		t.Fatal(err)
	}
	shutdownIndex := slices.Index(calls, "shutdown")
	deleteIndex := slices.Index(calls, "delete")
	if shutdownIndex < 0 || deleteIndex < 0 || shutdownIndex >= deleteIndex {
		t.Fatalf("remove call order = %v", calls)
	}
	maintenanceIndex := slices.Index(calls[shutdownIndex+1:], "maintenance")
	if maintenanceIndex < 0 || shutdownIndex+1+maintenanceIndex >= deleteIndex {
		t.Fatalf("remove did not acquire maintenance after shutdown and before delete: %v", calls)
	}
}

func newRegistrationTestManager(t *testing.T, home string) *Manager {
	t.Helper()
	manager, err := New(Options{
		HomeDir:      home,
		Executable:   os.Args[0],
		StartTimeout: time.Second,
		StopTimeout:  time.Second,
		ProbeTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func writeFleetTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
