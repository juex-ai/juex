package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/statusapi"
)

const (
	runtimeReadRetryWindow     = time.Second
	restartStatusRetryInterval = 50 * time.Millisecond
	restartConfirmationMaxWait = 5 * time.Second
)

func (m *Manager) Start(ctx context.Context, selector string) (AgentStatus, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentStatus{}, err
	}
	guard, err := acquireLifecycleLock(m.homeDir, entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	defer func() { _ = guard.Close() }()
	entry, err = m.reload(entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	return m.startEntry(ctx, entry)
}

func (m *Manager) startEntry(ctx context.Context, entry agentstate.RegistryEntry) (AgentStatus, error) {
	status := m.inspectStatus(ctx, entry)
	if err := startableConflict(entry, status); err != nil {
		return status, err
	}
	if status.RuntimeHealth == RuntimeHealthy {
		return status, nil
	}
	switch status.RuntimeHealth {
	case RuntimeUnhealthy:
		runtimeState, err := m.deps.readRuntime(entry.Dir)
		if err != nil {
			return status, &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("re-read stale runtime: %v", err)}
		}
		if err := m.cleanStaleRuntime(ctx, entry, runtimeState); err != nil {
			return status, err
		}
	case RuntimeStopped:
	default:
		return status, &ConflictError{AgentID: entry.ID, Reason: "runtime ownership is ambiguous; refusing to start another process"}
	}

	process, err := m.deps.spawn(m.executable, m.homeDir, entry)
	if err != nil {
		return status, err
	}
	deadline := time.NewTimer(m.startTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var runtimeReadFailureSince time.Time
	for {
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case err := <-process.Done:
			return status, fmt.Errorf("fleet: agent %q exited before readiness: %v (log: %s)", entry.ID, err, process.LogPath)
		case <-deadline.C:
			return status, fmt.Errorf("fleet: agent %q did not become ready within %s (log: %s)", entry.ID, m.startTimeout, process.LogPath)
		case <-ticker.C:
			runtimeState, err := m.deps.readRuntime(entry.Dir)
			if errors.Is(err, os.ErrNotExist) {
				runtimeReadFailureSince = time.Time{}
				continue
			}
			if err != nil {
				now := time.Now()
				if runtimeReadFailureSince.IsZero() {
					runtimeReadFailureSince = now
				}
				if now.Sub(runtimeReadFailureSince) < runtimeReadRetryWindow {
					continue
				}
				return status, fmt.Errorf("fleet: agent %q published invalid runtime metadata: %w", entry.ID, err)
			}
			runtimeReadFailureSince = time.Time{}
			if runtimeState.PID != process.PID {
				return status, &ConflictError{
					AgentID: entry.ID,
					Reason:  fmt.Sprintf("runtime belongs to pid %d, spawned pid was %d", runtimeState.PID, process.PID),
				}
			}
			probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
			probeErr := m.deps.probe(probeCtx, runtimeState)
			cancel()
			if probeErr == nil {
				ready := m.inspectStatus(ctx, entry)
				if ready.RuntimeHealth == RuntimeHealthy {
					return ready, nil
				}
				continue
			}
			var mismatch *endpoint.IdentityMismatchError
			if errors.As(probeErr, &mismatch) {
				return status, &ConflictError{AgentID: entry.ID, Reason: probeErr.Error()}
			}
		}
	}
}

func (m *Manager) Stop(ctx context.Context, selector string) (AgentStatus, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentStatus{}, err
	}
	guard, err := acquireLifecycleLock(m.homeDir, entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	defer func() { _ = guard.Close() }()
	entry, err = m.reload(entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	return m.stopEntry(ctx, entry)
}

func (m *Manager) stopEntry(ctx context.Context, entry agentstate.RegistryEntry) (AgentStatus, error) {
	status, _, err := m.stopEntryMode(ctx, entry, false)
	return status, err
}

func (m *Manager) stopEntryForRestart(
	ctx context.Context,
	entry agentstate.RegistryEntry,
) (AgentStatus, bool, error) {
	return m.stopEntryMode(ctx, entry, true)
}

func (m *Manager) stopEntryMode(
	ctx context.Context,
	entry agentstate.RegistryEntry,
	restarting bool,
) (AgentStatus, bool, error) {
	status := m.inspectStatus(ctx, entry)
	switch status.RuntimeHealth {
	case RuntimeStopped:
		return status, false, nil
	case RuntimeUnhealthy:
		runtimeState, err := m.deps.readRuntime(entry.Dir)
		if err != nil {
			return status, false, &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("re-read stale runtime: %v", err)}
		}
		if err := m.cleanStaleRuntime(ctx, entry, runtimeState); err != nil {
			return status, false, err
		}
		return m.inspectStatus(ctx, entry), false, nil
	case RuntimeHealthy:
	default:
		return status, false, &ConflictError{AgentID: entry.ID, Reason: "runtime identity is not verified; refusing shutdown"}
	}

	runtimeState, err := m.deps.readRuntime(entry.Dir)
	if err != nil {
		return status, false, &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("re-read runtime before shutdown: %v", err)}
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	restartAcknowledged := false
	if restarting {
		restartAcknowledged, err = m.deps.requestRestart(shutdownCtx, runtimeState)
	} else {
		err = m.deps.requestShutdown(shutdownCtx, runtimeState)
	}
	cancel()
	if err != nil {
		return status, false, &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("verified shutdown request failed: %v", err)}
	}

	deadline := time.NewTimer(m.stopTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return status, restartAcknowledged, ctx.Err()
		case <-deadline.C:
			return status, restartAcknowledged, &ConflictError{
				AgentID: entry.ID,
				Reason:  fmt.Sprintf("verified process did not stop within %s", m.stopTimeout),
			}
		case <-ticker.C:
			current, readErr := m.deps.readRuntime(entry.Dir)
			alive, aliveErr := m.deps.processAlive(runtimeState.PID)
			if aliveErr != nil {
				return status, restartAcknowledged, &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("check stopping process: %v", aliveErr)}
			}
			if errors.Is(readErr, os.ErrNotExist) && !alive {
				return m.inspectStatus(ctx, entry), restartAcknowledged, nil
			}
			if readErr == nil && !current.Matches(runtimeState) {
				return status, restartAcknowledged, &ConflictError{AgentID: entry.ID, Reason: "runtime identity changed while stopping"}
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return status, restartAcknowledged, &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("runtime became invalid while stopping: %v", readErr)}
			}
			if !alive && readErr == nil {
				probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
				probeErr := m.deps.probe(probeCtx, runtimeState)
				cancel()
				if probeErr != nil && !probeErrorProvesReachable(probeErr) {
					if err := m.cleanStaleRuntime(ctx, entry, runtimeState); err != nil {
						return status, restartAcknowledged, err
					}
					return m.inspectStatus(ctx, entry), restartAcknowledged, nil
				}
			}
		}
	}
}

func (m *Manager) Restart(ctx context.Context, selector string) (RestartResult, error) {
	return m.restart(ctx, selector, false)
}

func (m *Manager) restart(
	ctx context.Context,
	selector string,
	requireEligible bool,
) (RestartResult, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return RestartResult{}, err
	}
	guard, err := acquireLifecycleLock(m.homeDir, entry.ID)
	if err != nil {
		return RestartResult{}, err
	}
	defer func() { _ = guard.Close() }()
	entry, err = m.reload(entry.ID)
	if err != nil {
		return RestartResult{}, err
	}
	status := m.inspectStatus(ctx, entry)
	result := RestartResult{AgentStatus: status}
	if requireEligible {
		if reason := restartSkipReason(status); reason != "" {
			return result, &restartSkippedError{Reason: reason}
		}
	}
	if err := startableConflict(entry, status); err != nil {
		return result, err
	}
	var interrupted *restartActivity
	if status.RuntimeHealth == RuntimeHealthy {
		runtimeState, readErr := m.deps.readRuntime(entry.Dir)
		if readErr != nil {
			result.Resume.Error = fmt.Sprintf("detect interrupted turn: read runtime: %v", readErr)
		} else {
			activityCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
			activity, activityErr := m.deps.readRestartActivity(activityCtx, runtimeState)
			cancel()
			if activityErr != nil {
				result.Resume.Error = fmt.Sprintf("detect interrupted turn: %v", activityErr)
			} else if activity.State == statusapi.ActivityWorking {
				interrupted = &activity
			}
		}
	}
	_, restartAcknowledged, err := m.stopEntryForRestart(ctx, entry)
	if err != nil {
		return result, err
	}
	entry, err = m.reload(entry.ID)
	if err != nil {
		return result, err
	}
	result.AgentStatus, err = m.startEntry(ctx, entry)
	if err != nil {
		return result, err
	}
	if interrupted == nil {
		return result, nil
	}
	if !restartAcknowledged {
		result.Resume.Error = "confirm interrupted turn: runtime restart intent was not acknowledged"
		return result, nil
	}
	runtimeState, readErr := m.deps.readRuntime(entry.Dir)
	if readErr != nil {
		result.Resume.Error = fmt.Sprintf("confirm interrupted turn: read runtime: %v", readErr)
		return result, nil
	}
	confirmed, confirmErr := m.confirmRestartInterrupted(
		ctx,
		runtimeState,
		*interrupted,
	)
	if confirmErr != nil {
		result.Resume.Error = fmt.Sprintf("confirm interrupted turn: %v", confirmErr)
		return result, nil
	}
	if !confirmed {
		return result, nil
	}
	result.Resume.Required = true
	result.Resume.SessionID = interrupted.SessionID
	resumeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	turnID, resumeErr := m.deps.postRestartResume(
		resumeCtx,
		runtimeState,
		result.Resume.SessionID,
		restartResumePrompt,
	)
	cancel()
	if resumeErr != nil {
		result.Resume.Error = fmt.Sprintf("submit restart continuation: %v", resumeErr)
		return result, nil
	}
	result.Resume.Sent = true
	result.Resume.TurnID = turnID
	return result, nil
}

func (m *Manager) confirmRestartInterrupted(
	ctx context.Context,
	runtimeState endpoint.Runtime,
	expected restartActivity,
) (bool, error) {
	wait := restartConfirmationMaxWait
	if m.startTimeout > 0 && m.startTimeout < wait {
		wait = m.startTimeout
	}
	confirmCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ticker := time.NewTicker(restartStatusRetryInterval)
	defer ticker.Stop()

	for {
		readCtx, cancelRead := context.WithTimeout(confirmCtx, m.probeTimeout)
		actual, err := m.deps.readRestartActivity(readCtx, runtimeState)
		cancelRead()
		if err != nil {
			return false, err
		}
		if actual.SessionID != "" || actual.TurnID != "" || actual.State != "" {
			if actual.SessionID != expected.SessionID || actual.TurnID != expected.TurnID {
				return false, fmt.Errorf(
					"replacement selected session %q turn %q, want session %q turn %q",
					actual.SessionID,
					actual.TurnID,
					expected.SessionID,
					expected.TurnID,
				)
			}
			if actual.TurnState != statusapi.TurnCancelled {
				return false, fmt.Errorf(
					"replacement turn state is %q, want %q",
					actual.TurnState,
					statusapi.TurnCancelled,
				)
			}
			if actual.TurnErrorKind != statusapi.StatusErrorRuntimeRestart {
				return false, fmt.Errorf(
					"replacement turn error kind is %q, want %q",
					actual.TurnErrorKind,
					statusapi.StatusErrorRuntimeRestart,
				)
			}
			return true, nil
		}

		select {
		case <-confirmCtx.Done():
			return false, fmt.Errorf(
				"replacement status did not restore session %q turn %q: %w",
				expected.SessionID,
				expected.TurnID,
				confirmCtx.Err(),
			)
		case <-ticker.C:
		}
	}
}

type restartSkippedError struct {
	Reason string
}

func (e *restartSkippedError) Error() string {
	return "fleet: restart skipped: " + e.Reason
}

func startableConflict(entry agentstate.RegistryEntry, status AgentStatus) error {
	if status.Binding != BindingBound {
		return &ConflictError{
			AgentID: entry.ID,
			Reason:  "cannot start agent with " + string(status.Binding) + " workspace binding",
		}
	}
	if !entry.Agent.Enabled {
		return &ConflictError{AgentID: entry.ID, Reason: "agent is disabled"}
	}
	return nil
}

func (m *Manager) cleanStaleRuntime(
	ctx context.Context,
	entry agentstate.RegistryEntry,
	expected endpoint.Runtime,
) error {
	maintenance, err := m.deps.acquireMaintenance(entry.Dir)
	if err != nil {
		return &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("acquire endpoint maintenance guard: %v", err)}
	}
	defer func() { _ = maintenance.Close() }()
	current, err := m.deps.readRuntime(entry.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("re-read runtime under maintenance guard: %v", err)}
	}
	if !current.Matches(expected) {
		return &ConflictError{AgentID: entry.ID, Reason: "runtime identity changed before stale cleanup"}
	}
	alive, err := m.deps.processAlive(current.PID)
	if err != nil || alive {
		return &ConflictError{AgentID: entry.ID, Reason: "recorded process is alive or could not be classified safely"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	probeErr := m.deps.probe(probeCtx, current)
	cancel()
	if probeErr == nil || probeErrorProvesReachable(probeErr) {
		return &ConflictError{AgentID: entry.ID, Reason: "recorded endpoint remains reachable"}
	}
	if err := m.deps.removeRuntime(entry.Dir, current); err != nil {
		return &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("remove stale runtime: %v", err)}
	}
	return nil
}

func probeErrorProvesReachable(err error) bool {
	var mismatch *endpoint.IdentityMismatchError
	var httpStatus *endpoint.HTTPStatusError
	return errors.As(err, &mismatch) || errors.As(err, &httpStatus)
}

func (m *Manager) resolve(selector string) (agentstate.RegistryEntry, error) {
	entries, err := m.deps.listRegistry(m.homeDir)
	if err != nil {
		return agentstate.RegistryEntry{}, err
	}
	return resolveSelector(entries, selector)
}

func (m *Manager) reload(agentID string) (agentstate.RegistryEntry, error) {
	entries, err := m.deps.listRegistry(m.homeDir)
	if err != nil {
		return agentstate.RegistryEntry{}, err
	}
	for _, entry := range entries {
		if entry.ID == agentID {
			return entry, nil
		}
	}
	return agentstate.RegistryEntry{}, &NotFoundError{Selector: agentID}
}
