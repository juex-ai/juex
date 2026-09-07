package app

import (
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/modulecatalog"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

type moduleUnavailableError struct{ ModuleID string }

func (e *moduleUnavailableError) Error() string { return e.ModuleID + " module is disabled" }

// CheckTurnCapability lets transports reject disabled execution before opening
// a Thread. Storage and host maintenance remain available independently.
func CheckTurnCapability(cfg config.Config, threadID string, req TurnAdmissionRequest) error {
	if req.Kind == "" && len(req.Attachments) == 0 {
		if cmd, handled, err := ParseSlashCommand(req.Prompt); handled && err == nil {
			switch cmd.Name {
			case SlashStatus, SlashNew, SlashCompact:
				return nil
			case SlashGoal:
				if !cfg.ModuleEnabled(modulecatalog.Goal) {
					return &moduleUnavailableError{ModuleID: modulecatalog.Goal}
				}
			}
		}
	}
	return workerExecutionError(cfg, threadID)
}

func workerExecutionError(cfg config.Config, threadID string) error {
	if threadID != "" && threadID != thread.MainID && !cfg.ModuleEnabled(modulecatalog.WorkerThreads) {
		return &moduleUnavailableError{ModuleID: modulecatalog.WorkerThreads}
	}
	return nil
}

func (a *App) executionError() error {
	if a == nil || a.Engine == nil {
		return nil
	}
	snapshot := a.Engine.ThreadRuntimeSnapshot()
	if snapshot.Thread == nil {
		return nil
	}
	return workerExecutionError(a.cfg, snapshot.Thread.ID)
}

func moduleUnavailableResult(err error) TurnAdmissionResult {
	return rejectedResult("module_disabled", err.Error(), "", false, err, runtime.PendingInputStatus{})
}
