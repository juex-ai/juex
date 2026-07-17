package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
)

func (m *Manager) Status(ctx context.Context) ([]AgentStatus, error) {
	entries, err := m.deps.listRegistry(m.homeDir)
	if err != nil {
		return nil, err
	}
	statuses := make([]AgentStatus, 0, len(entries))
	for _, entry := range entries {
		statuses = append(statuses, m.inspectStatus(ctx, entry))
	}
	return statuses, nil
}

func (m *Manager) inspectStatus(ctx context.Context, entry agentstate.RegistryEntry) AgentStatus {
	status := AgentStatus{
		ID:        entry.ID,
		Name:      entry.Agent.Name,
		Workspace: entry.Agent.Workspace,
		Enabled:   entry.Agent.Enabled,
		Autostart: entry.Agent.Autostart,
	}
	binding := m.deps.inspectBinding(entry)
	switch binding.Kind {
	case agentstate.WorkspaceBound:
		status.Binding = BindingBound
	case agentstate.WorkspaceOrphaned:
		status.Binding = BindingOrphaned
		status.Problem = appendProblem(status.Problem, binding.Reason)
	default:
		status.Binding = BindingInvalid
		status.Problem = appendProblem(status.Problem, binding.Reason)
	}
	if entry.Problem != "" {
		status.Binding = BindingInvalid
		status.RuntimeHealth = RuntimeAmbiguous
		status.Problem = appendProblem(status.Problem, entry.Problem)
		return status
	}

	runtimeState, err := m.deps.readRuntime(entry.Dir)
	if errors.Is(err, os.ErrNotExist) {
		guard, guardErr := m.deps.acquireMaintenance(entry.Dir)
		if guardErr == nil {
			_ = guard.Close()
			status.RuntimeHealth = RuntimeStopped
			return status
		}
		var running *endpoint.AgentAlreadyRunningError
		if errors.As(guardErr, &running) {
			status.RuntimeHealth = RuntimeAmbiguous
			status.Problem = appendProblem(status.Problem, "endpoint is starting or has not published runtime metadata")
			return status
		}
		status.RuntimeHealth = RuntimeAmbiguous
		status.Problem = appendProblem(status.Problem, fmt.Sprintf("check endpoint maintenance guard: %v", guardErr))
		return status
	}
	if err != nil {
		status.RuntimePresent = true
		status.RuntimeHealth = RuntimeAmbiguous
		status.Problem = appendProblem(status.Problem, fmt.Sprintf("read runtime metadata: %v", err))
		return status
	}

	status.RuntimePresent = true
	status.PID = runtimeState.PID
	status.Endpoint = runtimeState.Endpoint
	status.StartedAt = runtimeState.StartedAt
	alive, aliveErr := m.deps.processAlive(runtimeState.PID)
	if aliveErr != nil {
		status.RuntimeHealth = RuntimeAmbiguous
		status.Problem = appendProblem(status.Problem, fmt.Sprintf("check process %d: %v", runtimeState.PID, aliveErr))
		return status
	}
	status.ProcessAlive = alive

	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	probeErr := m.deps.probe(probeCtx, runtimeState)
	cancel()
	if probeErr == nil {
		status.EndpointReachable = true
		status.EndpointMatched = true
	} else {
		var mismatch *endpoint.IdentityMismatchError
		var httpStatus *endpoint.HTTPStatusError
		status.EndpointReachable = errors.As(probeErr, &mismatch) || errors.As(probeErr, &httpStatus)
		status.Problem = appendProblem(status.Problem, fmt.Sprintf("probe endpoint: %v", probeErr))
	}

	switch {
	case status.ProcessAlive && status.EndpointMatched:
		status.RuntimeHealth = RuntimeHealthy
	case !status.ProcessAlive && !status.EndpointReachable:
		status.RuntimeHealth = RuntimeUnhealthy
		status.Problem = appendProblem(status.Problem, "recorded process and endpoint are not alive")
	default:
		status.RuntimeHealth = RuntimeAmbiguous
	}
	return status
}

func appendProblem(existing, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return next
	}
	return existing + "; " + next
}
