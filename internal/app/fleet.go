package app

import (
	"errors"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleet/services"
)

// NewFleet installs application configuration policy without moving lifecycle
// locking or restart ownership out of Fleet.
func NewFleet(opts fleet.Options) (*fleet.Manager, error) {
	opts.ConfigWriter = writeFleetAgentConfig
	opts.ConfigUpdater = func(home, id string, content []byte, revision string) error {
		_, err := config.WriteAgentConfigIfRevision(modulecatalog.Inventory(), content, home, id, revision, ValidateModuleConfig)
		var conflict *config.ConfigRevisionConflict
		if errors.As(err, &conflict) {
			return &fleet.ConflictError{AgentID: id, Reason: conflict.Error()}
		}
		var invalid *config.AgentConfigValidationError
		if errors.As(err, &invalid) {
			return &fleet.ConfigValidationError{Err: invalid.Err}
		}
		return err
	}
	opts.SupervisorTemplate = supervisorTemplate
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

// NewFleetServices binds service definitions exclusively to their owning Home.
func NewFleetServices(home string) (*services.Manager, error) {
	cfg, err := config.LoadHomeFleetConfigForHome(modulecatalog.Inventory(), home)
	if err != nil {
		return nil, err
	}
	return services.New(services.Options{Home: home, Definitions: cfg.Services})
}

func supervisorTemplate() ([]byte, []byte) {
	return []byte("preset: standard\nfleet_client:\n  profile: supervisor\n"), []byte(`# Supervisor

Help the user understand JueX and manage this Fleet's Agents. Use the typed Fleet tools for management. Your profile and identity are fixed at startup. Only your Main Thread owns management tools; Workers may research but cannot administer Agents.

Inspect current configuration and its revision before editing. Preserve user customizations. Use complete configuration validation. Report saved, published, applied, restarted, deferred, and behavior-verified outcomes separately. A healthy Runtime proves readiness, not task behavior. On deferred work, tell the user that application requires an explicit retry when idle. Set interrupt only when the user explicitly wants to interrupt active work.

Do not stop, disable, restart, reset, or change your own configuration during a tool call. Direct the user to external Fleet Supervisor controls. Do not automatically optimize Agents, run generic background tasks, or claim Memory maintenance capability before that service is available.
`)
}
