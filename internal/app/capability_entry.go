package app

import (
	"github.com/juex-ai/juex/internal/app/config"
	goalmodule "github.com/juex-ai/juex/internal/features/goal"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type moduleUnavailableError struct{ ModuleID string }

func (e *moduleUnavailableError) Error() string { return e.ModuleID + " module is disabled" }

// CheckTurnCapability lets transports reject disabled execution before opening
// a Thread. Storage and host maintenance remain available independently.
func CheckTurnCapability(cfg config.Config, threadID string, req agent.TurnAdmissionRequest) error {
	if req.Kind == "" && len(req.Attachments) == 0 {
		if cmd, handled, err := ParseSlashCommand(req.Prompt); handled && err == nil {
			switch cmd.Name {
			case SlashStatus, SlashNew, SlashCompact:
				return nil
			case SlashGoal:
				if !cfg.ModuleEnabled(goalmodule.ModuleID) {
					return &moduleUnavailableError{ModuleID: goalmodule.ModuleID}
				}
			}
		}
	}
	return workerExecutionError(cfg, threadID)
}

func workerExecutionError(cfg config.Config, threadID string) error {
	if threadID != "" && threadID != thread.MainID && !cfg.ModuleEnabled(workerthreadsmodule.ModuleID) {
		return &moduleUnavailableError{ModuleID: workerthreadsmodule.ModuleID}
	}
	return nil
}
