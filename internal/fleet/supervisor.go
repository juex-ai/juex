package fleet

import (
	"context"
	"fmt"
)

func (m *Manager) Serve(ctx context.Context, report func(Action)) error {
	guard, err := acquireSupervisorLock(m.homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()
	if report == nil {
		report = func(Action) {}
	}

	entries, err := m.deps.listRegistry(m.homeDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		status := m.inspectStatus(ctx, entry)
		switch status.RuntimeHealth {
		case RuntimeHealthy:
			report(Action{AgentID: entry.ID, Kind: "adopted", Detail: "verified existing runtime"})
			continue
		case RuntimeAmbiguous:
			report(Action{
				AgentID: entry.ID,
				Kind:    "failed",
				Detail:  status.Problem,
				Err:     &ConflictError{AgentID: entry.ID, Reason: "runtime ownership is ambiguous"},
			})
			continue
		case RuntimeUnhealthy:
			lifecycle, lockErr := acquireLifecycleLock(m.store(), entry.ID)
			if lockErr != nil {
				report(Action{AgentID: entry.ID, Kind: "failed", Err: lockErr})
				continue
			}
			runtimeState, readErr := m.deps.readRuntime(entry.Address)
			if readErr == nil {
				readErr = m.cleanStaleRuntime(ctx, entry, runtimeState)
			}
			_ = lifecycle.Close()
			if readErr != nil {
				report(Action{AgentID: entry.ID, Kind: "failed", Err: readErr})
				continue
			}
			report(Action{AgentID: entry.ID, Kind: "cleaned", Detail: "removed confirmed stale runtime metadata"})
		}

		if status.Binding == BindingBound && status.Enabled && status.Autostart {
			started, startErr := m.Start(ctx, entry.ID)
			if startErr != nil {
				report(Action{AgentID: entry.ID, Kind: "failed", Err: startErr})
				continue
			}
			report(Action{
				AgentID: entry.ID,
				Kind:    "started",
				Detail:  fmt.Sprintf("pid %d at %s", started.PID, started.Endpoint),
			})
			continue
		}
		report(Action{AgentID: entry.ID, Kind: "skipped", Detail: reconciliationSkipReason(status)})
	}
	report(Action{Kind: "ready", Detail: "startup reconciliation complete"})
	<-ctx.Done()
	return nil
}

func reconciliationSkipReason(status AgentStatus) string {
	switch {
	case status.Binding != BindingBound:
		return "workspace binding is " + string(status.Binding)
	case !status.Enabled:
		return "agent is disabled"
	case !status.Autostart:
		return "autostart is disabled"
	default:
		return "no startup action required"
	}
}
