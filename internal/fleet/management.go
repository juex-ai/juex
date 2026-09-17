package fleet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/fleetclient"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/endpoint"
)

type ManagementDeniedError struct{ Reason string }

func (e *ManagementDeniedError) Error() string { return "fleet management: " + e.Reason }

func (m *Manager) AuthorizeManagement(caller fleetclient.Caller) error {
	if caller.Profile != fleetclient.ProfileSupervisor {
		return &ManagementDeniedError{Reason: "supervisor profile required"}
	}
	binding, err := m.readSupervisor()
	if err != nil {
		return err
	}
	if binding.State != "ready" || binding.Agent.ID != caller.AgentID {
		return &ManagementDeniedError{Reason: "caller is not the bound Supervisor"}
	}
	entry, err := m.reload(caller.AgentID)
	if err != nil {
		return err
	}
	if !entry.Agent.Enabled {
		return &ManagementDeniedError{Reason: "Supervisor is disabled"}
	}
	return nil
}
func managementAgent(status AgentStatus) fleetclient.Agent {
	return fleetclient.Agent{ID: status.ID, Name: status.Name, Workspace: status.Workspace, Enabled: status.Enabled, Autostart: status.Autostart, RuntimeHealth: string(status.RuntimeHealth), Problem: status.Problem}
}
func (m *Manager) ManagedAgents(ctx context.Context, caller fleetclient.Caller) ([]fleetclient.Agent, error) {
	if err := m.AuthorizeManagement(caller); err != nil {
		return nil, err
	}
	statuses, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]fleetclient.Agent, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, managementAgent(status))
	}
	return items, nil
}
func (m *Manager) ManagedCreate(ctx context.Context, caller fleetclient.Caller, request fleetclient.CreateRequest) (fleetclient.Result, error) {
	if err := m.AuthorizeManagement(caller); err != nil {
		return fleetclient.Result{}, err
	}
	opts := AddOptions{Workspace: request.Workspace}
	if request.Name != "" {
		opts.Name = &request.Name
	}
	added, err := m.Add(ctx, opts)
	if err != nil {
		return fleetclient.Result{}, err
	}
	result := fleetclient.Result{Agent: managementAgent(added.Agent), Saved: true, Published: true}
	if request.Start {
		status, startErr := m.Start(ctx, added.Agent.ID)
		result.Agent = managementAgent(status)
		result.Applied = startErr == nil
		if startErr != nil {
			result.Error = startErr.Error()
		}
	}
	return result, nil
}
func revision(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

func (m *Manager) ManagedConfig(ctx context.Context, caller fleetclient.Caller, id string) (fleetclient.Config, error) {
	if err := m.AuthorizeManagement(caller); err != nil {
		return fleetclient.Config{}, err
	}
	entry, err := m.resolve(id)
	if err != nil {
		return fleetclient.Config{}, err
	}
	state, err := m.Config(entry.ID)
	if err != nil {
		return fleetclient.Config{}, err
	}
	currentRevision := revision(state.Content)
	redacted, err := RedactAgentConfig(state)
	if err != nil {
		return fleetclient.Config{}, err
	}
	restartRequired := state.Exists
	if runtimeState, err := m.Endpoint(ctx, entry.ID); err == nil {
		probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
		actual, inspectErr := endpoint.Inspect(probeCtx, runtimeState)
		cancel()
		restartRequired = inspectErr != nil || actual.ConfigRevision != currentRevision
	}
	return fleetclient.Config{Content: redacted.Content, Revision: currentRevision, Exists: state.Exists, RestartRequired: restartRequired}, nil
}
func (m *Manager) ManagedConfigure(ctx context.Context, caller fleetclient.Caller, id string, request fleetclient.ConfigRequest) (fleetclient.Result, error) {
	if err := m.AuthorizeManagement(caller); err != nil {
		return fleetclient.Result{}, err
	}
	entry, err := m.resolve(id)
	if err != nil {
		return fleetclient.Result{}, err
	}
	if entry.ID == caller.AgentID {
		return fleetclient.Result{}, &ManagementDeniedError{Reason: "self configuration must be applied through external Fleet operations"}
	}
	guard, err := acquireLifecycleLock(m.store(), entry.ID)
	if err != nil {
		return fleetclient.Result{}, err
	}
	defer func() { _ = guard.Close() }()
	entry, err = m.reload(entry.ID)
	if err != nil {
		return fleetclient.Result{}, err
	}
	current, err := readAgentConfig(entry)
	if err != nil {
		return fleetclient.Result{}, err
	}
	if request.ExpectedRevision == "" || request.ExpectedRevision != revision(current.Content) {
		return fleetclient.Result{}, &ConflictError{AgentID: entry.ID, Reason: "configuration revision changed; reload before applying"}
	}
	content, err := mergeRedactedEnvironmentValues([]byte(request.Content), current)
	if err != nil {
		return fleetclient.Result{}, err
	}
	if m.configUpdater == nil {
		return fleetclient.Result{}, errors.New("fleet: revision-aware config publisher is unavailable")
	}
	if err := m.configUpdater(m.homeDir, entry.ID, content, request.ExpectedRevision); err != nil {
		return fleetclient.Result{}, err
	}
	result := fleetclient.Result{Saved: true, Published: true, RestartRequired: true, Revision: revision(string(content)), Agent: managementAgent(m.inspectStatus(ctx, entry))}
	if !request.Apply {
		return result, nil
	}
	status, err := m.applyManagedLifecycle(ctx, entry, "restart", request.Interrupt)
	result.Agent = managementAgent(status)
	if errors.Is(err, endpoint.ErrRuntimeBusy) {
		result.Deferred = true
		return result, nil
	}
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	result.Restarted = true
	runtimeState, err := m.deps.readRuntime(entry.Address)
	if err == nil {
		var actual endpoint.Runtime
		probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
		actual, err = endpoint.Inspect(probeCtx, runtimeState)
		cancel()
		if err == nil && actual.ConfigRevision != result.Revision {
			err = errors.New("runtime loaded a different configuration revision; retry application")
		}
	}
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	result.Applied = true
	latest, readErr := readAgentConfig(entry)
	result.RestartRequired = readErr != nil || revision(latest.Content) != result.Revision
	if readErr != nil {
		result.Error = readErr.Error()
	}
	return result, nil
}

func (m *Manager) ManagedLifecycle(ctx context.Context, caller fleetclient.Caller, id string, request fleetclient.LifecycleRequest) (fleetclient.Result, error) {
	if err := m.AuthorizeManagement(caller); err != nil {
		return fleetclient.Result{}, err
	}
	entry, err := m.resolve(id)
	if err != nil {
		return fleetclient.Result{}, err
	}
	if entry.ID == caller.AgentID {
		return fleetclient.Result{}, &ManagementDeniedError{Reason: "self lifecycle must be controlled through external Fleet operations"}
	}
	guard, err := acquireLifecycleLock(m.store(), entry.ID)
	if err != nil {
		return fleetclient.Result{}, err
	}
	defer func() { _ = guard.Close() }()
	entry, err = m.reload(entry.ID)
	if err != nil {
		return fleetclient.Result{}, err
	}
	status, err := m.applyManagedLifecycle(ctx, entry, request.Action, request.Interrupt)
	result := fleetclient.Result{Agent: managementAgent(status), Applied: err == nil, Restarted: err == nil && request.Action == "restart"}
	if errors.Is(err, endpoint.ErrRuntimeBusy) {
		result.Deferred = true
		return result, nil
	}
	return result, err
}

func (m *Manager) applyManagedLifecycle(ctx context.Context, entry agentstate.RegistryEntry, action string, interrupt bool) (AgentStatus, error) {
	switch action {
	case "start":
		return m.startEntry(ctx, entry)
	case "enable":
		enabled := true
		if _, err := m.deps.updateAgent(m.homeDir, entry.ID, agentstate.AgentUpdate{Enabled: &enabled}); err != nil {
			return AgentStatus{}, err
		}
		entry, err := m.reload(entry.ID)
		if err != nil {
			return AgentStatus{}, err
		}
		return m.inspectStatus(ctx, entry), nil
	case "restart":
		if interrupt {
			result, err := m.restartEntry(ctx, entry, false)
			return result.AgentStatus, err
		}
		if err := startableConflict(entry, m.inspectStatus(ctx, entry)); err != nil {
			return AgentStatus{}, err
		}
	case "stop", "disable":
	default:
		return AgentStatus{}, &ValidationError{Reason: "unsupported management lifecycle action"}
	}
	status, _, err := m.stopEntryPolicy(ctx, entry, false, !interrupt)
	if err != nil {
		return status, err
	}
	if action == "restart" {
		return m.startEntry(ctx, entry)
	}
	if action == "disable" {
		enabled := false
		if _, err := m.deps.updateAgent(m.homeDir, entry.ID, agentstate.AgentUpdate{Enabled: &enabled}); err != nil {
			return status, err
		}
		entry, err = m.reload(entry.ID)
		if err != nil {
			return status, err
		}
		status = m.inspectStatus(ctx, entry)
	}
	return status, nil
}
