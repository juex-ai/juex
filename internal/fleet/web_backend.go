package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/endpoint"
)

type AgentConfig struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

type ConfigValidationError struct {
	Err error
}

func (e *ConfigValidationError) Error() string {
	return fmt.Sprintf("fleet: invalid workspace config: %v", e.Err)
}

func (e *ConfigValidationError) Unwrap() error {
	return e.Err
}

// Endpoint returns runtime metadata only after re-verifying that the selected
// agent still owns a reachable endpoint.
func (m *Manager) Endpoint(ctx context.Context, selector string) (endpoint.Runtime, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return endpoint.Runtime{}, err
	}
	status := m.inspectStatus(ctx, entry)
	if status.Binding != BindingBound || status.RuntimeHealth != RuntimeHealthy {
		return endpoint.Runtime{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  "agent does not have a verified healthy runtime",
		}
	}

	runtimeState, err := m.deps.readRuntime(entry.Dir)
	if err != nil {
		return endpoint.Runtime{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  fmt.Sprintf("re-read runtime before proxying: %v", err),
		}
	}
	alive, err := m.deps.processAlive(runtimeState.PID)
	if err != nil || !alive {
		return endpoint.Runtime{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  "runtime process is no longer alive",
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	probeErr := m.deps.probe(probeCtx, runtimeState)
	cancel()
	if probeErr != nil {
		return endpoint.Runtime{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  fmt.Sprintf("runtime endpoint is no longer verified: %v", probeErr),
		}
	}
	return runtimeState, nil
}

// ReadOnlyState resolves durable agent paths without requiring a running
// endpoint. Orphaned and invalid bindings are rejected before exposing paths.
func (m *Manager) ReadOnlyState(selector string) (ReadOnlyAgentState, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return ReadOnlyAgentState{}, err
	}
	binding := m.deps.inspectBinding(entry)
	if binding.Kind != agentstate.WorkspaceBound {
		return ReadOnlyAgentState{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  "cannot read sessions for an unbound workspace: " + binding.Reason,
		}
	}
	return ReadOnlyAgentState{
		ID:        entry.ID,
		Name:      entry.Agent.Name,
		Workspace: entry.Agent.Workspace,
		StateDir:  entry.Dir,
	}, nil
}

func (m *Manager) Config(selector string) (AgentConfig, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentConfig{}, err
	}
	status := m.inspectStatus(context.Background(), entry)
	if status.Binding != BindingBound {
		return AgentConfig{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  "cannot access config for " + string(status.Binding) + " workspace binding",
		}
	}
	return readAgentConfig(entry)
}

func (m *Manager) UpdateConfig(
	ctx context.Context,
	selector string,
	content []byte,
) (AgentConfig, AgentStatus, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentConfig{}, AgentStatus{}, err
	}
	guard, err := acquireLifecycleLock(m.store(), entry.ID)
	if err != nil {
		return AgentConfig{}, AgentStatus{}, err
	}
	defer func() { _ = guard.Close() }()

	entry, err = m.reload(entry.ID)
	if err != nil {
		return AgentConfig{}, AgentStatus{}, err
	}
	status := m.inspectStatus(ctx, entry)
	if err := startableConflict(entry, status); err != nil {
		return AgentConfig{}, status, err
	}
	if status.RuntimeHealth == RuntimeAmbiguous {
		return AgentConfig{}, status, &ConflictError{
			AgentID: entry.ID,
			Reason:  "runtime ownership is ambiguous; refusing config update",
		}
	}
	if _, err := config.ValidateWorkspaceConfig(content, entry.Agent.Workspace); err != nil {
		return AgentConfig{}, status, &ConfigValidationError{Err: err}
	}
	if _, err := config.WriteWorkspaceConfig(content, entry.Agent.Workspace); err != nil {
		return AgentConfig{}, status, err
	}
	configState, err := readAgentConfig(entry)
	if err != nil {
		return AgentConfig{}, status, err
	}
	if _, err := m.stopEntry(ctx, entry); err != nil {
		return configState, status, err
	}
	entry, err = m.reload(entry.ID)
	if err != nil {
		return configState, status, err
	}
	status, err = m.startEntry(ctx, entry)
	return configState, status, err
}

func readAgentConfig(entry agentstate.RegistryEntry) (AgentConfig, error) {
	path := (config.Config{WorkDir: entry.Agent.Workspace}).RuntimePaths().RuntimeConfigPath
	state := AgentConfig{Path: path}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fleet: read workspace config: %w", err)
	}
	state.Content = string(content)
	state.Exists = true
	return state, nil
}
