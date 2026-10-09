package migration

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// AgentConfigBindings carries explicit target choices. ModelID must identify
// the published source primary; that correspondence and tenant access are the
// caller's responsibility. Files is an explicit choice because the source's basic
// file and search modules do not match the target's grouped tools.
// GlobalInstructionPath is on the selected Execution environment. Instructions
// must be resolved explicitly, even when empty.
type AgentConfigBindings struct {
	ModelID               string
	Instructions          *string
	FilesEnabled          *bool
	ShellEnabled          *bool
	CalendarEnabled       *bool
	CollaborationEnabled  *bool
	GlobalInstructionPath string
}

// ConvertAgentConfig supplies initial CreateAgent configuration, before hooks
// and other resources are bound. It is not an update patch: ConfigureAgent would
// clear existing hooks. Lifecycle, Workspace, resources and Memory profile
// require separate conversion and acceptance.
func ConvertAgentConfig(source ResolvedConfig, definition legacy.AgentDefinition, bindings AgentConfigBindings) (management.AgentConfig, error) {
	id, err := uuid.Parse(bindings.ModelID)
	if err != nil || id == uuid.Nil || id.String() != bindings.ModelID {
		return management.AgentConfig{}, errors.New("agent configuration requires a published model identity")
	}
	return prepareAgentConfig(source, definition, bindings)
}

// Preparation validates policy before publication allocates a model UUID.
func prepareAgentConfig(source ResolvedConfig, definition legacy.AgentDefinition, bindings AgentConfigBindings) (management.AgentConfig, error) {
	if strings.TrimSpace(source.AgentID) == "" || source.AgentID != definition.ID {
		return management.AgentConfig{}, errors.New("agent configuration requires matching source identity")
	}
	if len(source.Modules) != len(sourceModules) {
		return management.AgentConfig{}, errors.New("agent configuration requires complete resolved source modules")
	}
	for name := range sourceModules {
		if _, ok := source.Modules[name]; !ok {
			return management.AgentConfig{}, errors.New("agent configuration has missing or unknown source modules")
		}
	}
	if (source.WorkerDepth != 1 && source.WorkerDepth != 2) || bindings.ShellEnabled == nil || bindings.FilesEnabled == nil || bindings.CalendarEnabled == nil || bindings.CollaborationEnabled == nil || bindings.Instructions == nil {
		return management.AgentConfig{}, errors.New("agent configuration requires resolved depth, instructions and explicit file, shell, application and collaboration policies")
	}
	instructions := instructionpolicy.DynamicInstructions{Enabled: source.Modules["agents-md"], GlobalPath: bindings.GlobalInstructionPath}
	if instructions.Enabled && !*bindings.FilesEnabled {
		return management.AgentConfig{}, errors.New("dynamic guidance without Files requires an explicit target policy change")
	}
	if (instructions.Enabled && source.UserResources) != (instructions.GlobalPath != "") {
		return management.AgentConfig{}, errors.New("global guidance binding must match enabled source guidance and user resources")
	}
	// Source hooks and command observers launched independently of its shell
	// module. Target dispatch requires Shell; reject unresolved combinations
	// before an imported Hook could be skipped by Runtime.
	if !*bindings.ShellEnabled && (source.Modules["hooks"] || source.Modules["observables"]) {
		return management.AgentConfig{}, errors.New("source hooks and command observers require target Shell or separate resource-policy conversion")
	}
	policy := agentpolicy.Policy{}
	for _, pair := range []struct {
		capability agentpolicy.Capability
		enabled    bool
	}{
		{agentpolicy.Files, *bindings.FilesEnabled},
		{agentpolicy.Shell, *bindings.ShellEnabled},
		{agentpolicy.Workers, source.Modules["worker-threads"]},
		{agentpolicy.MCP, source.Modules["mcp"]},
		{agentpolicy.Observations, source.Modules["observables"]},
		{agentpolicy.Memory, source.Modules["memory"]},
		{agentpolicy.Hooks, source.Modules["hooks"]},
		{agentpolicy.Extensions, source.Modules["extensions"]},
		{agentpolicy.Notes, source.Modules["notes"]},
		{agentpolicy.Tasks, source.Modules["tasks"]},
		{agentpolicy.ContextControl, source.Modules["context-control"]},
		{agentpolicy.WorkingFiles, source.Modules["scratchpad"]},
		// The old fleet-management module administered processes as Supervisor;
		// it did not grant the target's cross-Agent messaging authority.
		{agentpolicy.Collaboration, *bindings.CollaborationEnabled},
		{agentpolicy.Calendar, *bindings.CalendarEnabled},
	} {
		if !pair.enabled {
			policy.Disabled = append(policy.Disabled, pair.capability)
		}
	}
	policy = policy.Normalized()
	config := management.AgentConfig{Name: definition.Name, ModelID: bindings.ModelID, Instructions: *bindings.Instructions, WorkerDepth: source.WorkerDepth, Capabilities: &policy, DynamicInstructions: &instructions}
	if err := config.Validate(); err != nil {
		return management.AgentConfig{}, err
	}
	return config, nil
}
