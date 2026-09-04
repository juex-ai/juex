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
	metricKeys := make([]string, 0, len(entries))
	for _, entry := range entries {
		metricKeys = append(metricKeys, entry.ID)
		statuses = append(statuses, m.inspectStatus(ctx, entry))
	}
	if m.processMetrics != nil {
		m.processMetrics.Retain(metricKeys)
	}
	return statuses, nil
}

// ResolveAgent selects one Registry entry without requiring a live Workspace
// binding. Callers must leave lifecycle and path-safety checks to the operation
// they invoke with the returned ID.
func (m *Manager) ResolveAgent(selector string) (AgentReference, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentReference{}, err
	}
	return AgentReference{
		ID:        entry.ID,
		Name:      entry.Agent.Name,
		Workspace: entry.Agent.Workspace,
	}, nil
}

// StatusOne resolves and inspects exactly one Agent without scanning the Fleet.
func (m *Manager) StatusOne(ctx context.Context, selector string) (AgentStatus, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentStatus{}, err
	}
	return m.inspectStatus(ctx, entry), nil
}

func (m *Manager) inspectStatus(ctx context.Context, entry agentstate.RegistryEntry) AgentStatus {
	status := AgentStatus{
		ID:        entry.ID,
		Name:      entry.Agent.Name,
		Workspace: entry.Agent.Workspace,
		Enabled:   entry.Agent.Enabled,
		Autostart: entry.Agent.Autostart,
	}
	if m.processMetrics != nil {
		defer func() {
			if status.RuntimeHealth != RuntimeHealthy {
				m.processMetrics.Forget(status.ID)
			}
		}()
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

	runtimeState, err := m.deps.readRuntime(entry.Address)
	if errors.Is(err, os.ErrNotExist) {
		guard, guardErr := m.deps.acquireMaintenance(entry.Address)
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
	status.BinaryVersion = runtimeState.BinaryVersion
	process := m.inspectRecordedProcess(runtimeState)
	if !process.ExistenceKnown {
		status.RuntimeHealth = RuntimeAmbiguous
		status.Problem = appendProblem(status.Problem, process.Err.Error())
		return status
	}
	status.ProcessAlive = process.Alive
	processReplaced := process.Identity == processIdentityReplaced

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
	case status.ProcessAlive && status.EndpointMatched && acceptsRecordedProcessIdentity(runtimeState, process):
		status.RuntimeHealth = RuntimeHealthy
	case processReplaced && status.EndpointMatched:
		status.RuntimeHealth = RuntimeAmbiguous
		status.Problem = appendProblem(status.Problem, "process identity conflicts with exact endpoint identity")
	case processReplaced && !status.EndpointMatched:
		status.RuntimeHealth = RuntimeUnhealthy
		status.Problem = appendProblem(status.Problem, "recorded PID is now owned by a different process")
	case !status.ProcessAlive && !status.EndpointReachable:
		status.RuntimeHealth = RuntimeUnhealthy
		status.Problem = appendProblem(status.Problem, "recorded process and endpoint are not alive")
	default:
		status.RuntimeHealth = RuntimeAmbiguous
		if process.Err != nil {
			status.Problem = appendProblem(status.Problem, process.Err.Error())
		}
	}
	if m.processMetrics != nil {
		if status.RuntimeHealth == RuntimeHealthy {
			usage, sampleErr := m.processMetrics.Sample(ctx, status.ID, status.PID)
			if sampleErr == nil {
				status.Process = &usage
			}
		}
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
