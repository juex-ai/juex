package agent

import (
	"github.com/juex-ai/juex/internal/framework/runtime"
)

func (a *Agent) executionError() error {
	if a == nil || a.Engine == nil {
		return nil
	}
	snapshot := a.Engine.ThreadRuntimeSnapshot()
	if snapshot.Thread == nil {
		return nil
	}
	if a.executionPolicy.CheckExecution == nil {
		return nil
	}
	return a.executionPolicy.CheckExecution(snapshot.Thread.ID)
}

func moduleUnavailableResult(err error) TurnAdmissionResult {
	return rejectedResult("module_disabled", err.Error(), "", false, err, runtime.PendingInputStatus{})
}
