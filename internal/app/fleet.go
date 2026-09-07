package app

import (
	"errors"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/fleet"
)

// NewFleet installs application configuration policy without moving lifecycle
// locking or restart ownership out of Fleet.
func NewFleet(opts fleet.Options) (*fleet.Manager, error) {
	opts.ConfigWriter = writeFleetAgentConfig
	return fleet.New(opts)
}

func writeFleetAgentConfig(homeDir, agentID string, content []byte) error {
	_, err := config.WriteAgentConfig(modulecatalog.Inventory(), content, homeDir, agentID, ValidateModuleConfig)
	var validation *config.AgentConfigValidationError
	if errors.As(err, &validation) {
		return &fleet.ConfigValidationError{Err: validation.Err}
	}
	return err
}

// InspectFleetAgent resolves passive Module selection from a verified durable
// address; it never constructs runtime resources or repairs import state.
func InspectFleetAgent(state fleet.ReadOnlyAgentState) (config.Config, error) {
	selection, err := config.ReadModuleInspectionConfig(modulecatalog.Inventory(), state.Workspace, state.HomeDir, state.ConfigPath)
	return config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: state.Workspace,
		Preset: selection.Preset, Modules: selection.Modules, AgentID: state.ID, AgentName: state.Name, AgentStateDir: state.StateDir}, err
}
