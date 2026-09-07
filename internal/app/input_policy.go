package app

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/framework/agent"
)

func (a *App) applicationInputPolicy(cfg config.Config) agent.InputPolicy {
	return agent.InputPolicy{
		CheckInput:     func(id string, req agent.TurnAdmissionRequest) error { return CheckTurnCapability(cfg, id, req) },
		CheckExecution: func(id string) error { return workerExecutionError(cfg, id) },
		ParseCommand:   ParseSlashCommand,
		CommandHelp:    AvailableSlashCommandsText(),
		Status: func() (agent.CommandStatus, error) {
			status := a.StatusSnapshot()
			data, err := json.Marshal(status)
			return agent.CommandStatus{JSON: data, Text: status.Text(), GenerationID: status.GenerationID}, err
		},
		AttachmentWarnings: a.AttachmentWarnings,
		NewContextInput:    NewThreadGreetingMessage(),
	}
}

func (a *App) checkInput(id string, request agent.TurnAdmissionRequest) error {
	if a.executionPolicy.CheckInput == nil {
		return nil
	}
	return a.executionPolicy.CheckInput(id, request)
}

func (a *App) parseCommand(input string) (agent.Command, bool, error) {
	if a.executionPolicy.ParseCommand == nil {
		return agent.Command{}, false, nil
	}
	return a.executionPolicy.ParseCommand(input)
}

func (a *App) attachmentWarnings(count int) []agent.TurnWarning {
	if a.executionPolicy.AttachmentWarnings == nil {
		return nil
	}
	return a.executionPolicy.AttachmentWarnings(count)
}
