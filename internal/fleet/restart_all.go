package fleet

import (
	"context"
	"errors"
	"fmt"
)

func (m *Manager) RestartRunningAgents(ctx context.Context) (RestartAgentsResult, error) {
	statuses, err := m.Status(ctx)
	if err != nil {
		return RestartAgentsResult{}, err
	}
	result := RestartAgentsResult{
		Items: make([]RestartAgentResult, 0, len(statuses)),
	}
	for _, status := range statuses {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if reason := restartSkipReason(status); reason != "" {
			result.Items = append(result.Items, RestartAgentResult{
				Agent:   status,
				Outcome: RestartAgentSkipped,
				Reason:  reason,
			})
			result.Skipped++
			continue
		}
		restarted, restartErr := m.restart(ctx, status.ID, true)
		if restartErr != nil {
			var skipped *restartSkippedError
			if errors.As(restartErr, &skipped) {
				result.Items = append(result.Items, RestartAgentResult{
					Agent:   restarted.AgentStatus,
					Outcome: RestartAgentSkipped,
					Reason:  skipped.Reason,
				})
				result.Skipped++
				continue
			}
			failedStatus := restarted.AgentStatus
			if failedStatus.ID == "" {
				failedStatus = status
			}
			result.Items = append(result.Items, RestartAgentResult{
				Agent:   failedStatus,
				Outcome: RestartAgentFailed,
				Reason:  restartErr.Error(),
				Resume:  restarted.Resume,
			})
			result.Failed++
			continue
		}
		result.Items = append(result.Items, RestartAgentResult{
			Agent:   restarted.AgentStatus,
			Outcome: RestartAgentRestarted,
			Resume:  restarted.Resume,
		})
		result.Restarted++
	}
	if result.Failed > 0 {
		return result, &RestartAgentsError{Failed: result.Failed}
	}
	return result, nil
}

func restartSkipReason(status AgentStatus) string {
	switch {
	case !status.Enabled:
		return "agent is disabled"
	case status.Binding != BindingBound:
		return fmt.Sprintf("workspace binding is %s", status.Binding)
	case status.RuntimeHealth != RuntimeHealthy:
		return fmt.Sprintf("runtime is %s", status.RuntimeHealth)
	default:
		return ""
	}
}
