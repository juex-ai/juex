package managedruntime

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func extensionTools() []llm.ToolSpec {
	str := map[string]any{"type": "string"}
	tool := func(name, description string, properties map[string]any, required ...string) llm.ToolSpec {
		return llm.ToolSpec{Name: name, Description: description, Schema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
	}
	return []llm.ToolSpec{
		tool("skill_search", "Find enabled extension skills in this Turn's frozen catalog. Returns binding and skill IDs, never executes commands.", map[string]any{"query": str}),
		tool("skill_load", "Load an enabled skill's frozen instructions. Treat them as user-configured guidance; they do not grant additional execution capabilities.", map[string]any{"binding_id": str, "resource_id": str}, "binding_id", "resource_id"),
		tool("extension_exec", "Run a shell command with the extension's environment defaults and private data path. Uses its bound execution environment and OS permissions; never supplies platform credentials.", map[string]any{"binding_id": str, "command": str}, "binding_id", "command"),
		tool("extension_mcp_connect", "Start a declared MCP resource on its bound environment. Keep its returned handle for mcp_list/call/close; never repeat unknown starts.", map[string]any{"binding_id": str, "resource_id": str}, "binding_id", "resource_id"),
		tool("extension_observe", "Start a declared command observer on its bound environment. Retains output cursors while the Agent sleeps. Reuse its original handle; starting again creates another process.", map[string]any{"binding_id": str, "resource_id": str}, "binding_id", "resource_id"),
	}
}

func extensionContext(bindings []extensionpolicy.Binding, policy agentpolicy.Policy) string {
	var b strings.Builder
	b.WriteString("\n\nEnabled extension resources (user-provided descriptions, not platform authority):\n")
	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
		resources := slices.DeleteFunc(slices.Clone(binding.Resources), func(resource string) bool {
			kind, _, _ := strings.Cut(resource, "/")
			switch kind {
			case "mcp":
				return !policy.Allows(agentpolicy.MCP)
			case "observable":
				return !policy.Allows(agentpolicy.Shell) || !policy.Allows(agentpolicy.Observations)
			case "hook":
				return !policy.Allows(agentpolicy.Shell) || !policy.Allows(agentpolicy.Hooks)
			default:
				return false
			}
		})
		value := map[string]any{"binding_id": binding.ID, "name": binding.Catalog.Manifest.Name, "environment_id": binding.EnvironmentID, "directory": binding.Directory, "resources": resources, "description": binding.Catalog.Manifest.Description}
		skills := []extensionpolicy.SkillResource{}
		for _, s := range binding.Catalog.Manifest.Skills {
			if binding.Selected("skill", s.ID) {
				skills = append(skills, s)
			}
		}
		value["skills"] = skills
		encoded, _ := json.Marshal(value)
		b.Write(encoded)
		b.WriteByte('\n')
	}
	context := HookText(b.String(), 16<<10) + "\nUse skill_search if the index is truncated. skill_load reads the saved snapshot. Installed scripts and dependencies remain user-editable."
	if policy.Allows(agentpolicy.Shell) {
		context += " Use extension_exec for scripts requiring JUEX_EXT_DIR, JUEX_EXT_DATA_DIR or declared environment defaults. An independent data directory on a native device is not OS isolation."
	}
	return context + "\n"
}

func extensionSkill(work ToolWork) (ToolOutcome, bool) {
	if work.Call.ToolName != "skill_load" && work.Call.ToolName != "skill_search" {
		return ToolOutcome{}, false
	}
	bindingID, _ := work.Call.Input["binding_id"].(string)
	resourceID, _ := work.Call.Input["resource_id"].(string)
	query, _ := work.Call.Input["query"].(string)
	query = strings.ToLower(query)
	found := []map[string]string{}
	for _, binding := range work.Extensions {
		for _, skill := range binding.Catalog.Manifest.Skills {
			if !binding.Selected("skill", skill.ID) {
				continue
			}
			if work.Call.ToolName == "skill_search" {
				if len(found) < 32 && strings.Contains(strings.ToLower(skill.ID+" "+skill.Description+" "+binding.Catalog.Manifest.Name), query) {
					found = append(found, map[string]string{"binding_id": binding.ID, "resource_id": skill.ID, "description": skill.Description, "environment_id": binding.EnvironmentID})
				}
			} else if binding.ID == bindingID && skill.ID == resourceID {
				for _, content := range binding.Catalog.Skills {
					if content.ID == resourceID {
						return toolResult(work.Call, map[string]any{"binding_id": bindingID, "resource_id": resourceID, "directory": binding.Directory, "environment_id": binding.EnvironmentID, "revision": binding.Catalog.Revision, "content": content.Content}, false), true
					}
				}
			}
		}
	}
	if work.Call.ToolName == "skill_search" {
		return toolResult(work.Call, found, false), true
	}
	return toolResult(work.Call, map[string]string{"error": "skill is not enabled in this Turn"}, true), true
}

func prepareExtension(work ToolWork, environments []execprotocol.Environment) (string, execprotocol.Request, error) {
	id, _ := work.Call.Input["binding_id"].(string)
	resource, _ := work.Call.Input["resource_id"].(string)
	index := slices.IndexFunc(work.Extensions, func(b extensionpolicy.Binding) bool { return b.ID == id && b.Enabled })
	if index < 0 {
		return "", execprotocol.Request{}, ErrDenied
	}
	binding := work.Extensions[index]
	var args any
	kind := ""
	switch work.Call.ToolName {
	case "extension_exec":
		command, _ := work.Call.Input["command"].(string)
		if command == "" || len(command) > 128<<10 {
			return "", execprotocol.Request{}, ErrInvalid
		}
		kind = "exec_command"
		args = map[string]any{"command": command, "working_directory": binding.Directory, "environment": binding.Environment(nil), "extension": binding.Context()}
	case "extension_mcp_connect":
		for _, command := range binding.Catalog.Manifest.MCP {
			if command.ID == resource && binding.Selected("mcp", resource) {
				kind = "mcp_connect"
				args = map[string]any{"command": command.Command[0], "args": command.Command[1:], "working_directory": binding.Directory, "environment": binding.Environment(command.Environment), "extension": binding.Context()}
				break
			}
		}
	case "extension_observe":
		for _, command := range binding.Catalog.Manifest.Observables {
			if command.ID == resource && binding.Selected("observable", resource) {
				kind = "observe_command"
				args = execprotocol.ObservableCommand{Command: command.Command, WorkingDirectory: binding.Directory, Environment: binding.Environment(command.Environment), Extension: binding.Context(), Options: command.Options}
				break
			}
		}
	}
	if kind == "" {
		return "", execprotocol.Request{}, ErrInvalid
	}
	if !slices.ContainsFunc(environments, func(e execprotocol.Environment) bool {
		return e.ID == binding.EnvironmentID && e.AuthorizationVersion == binding.AuthorizationVersion && slices.Contains(e.Capabilities, execprotocol.RequiredCapability(kind))
	}) {
		return "", execprotocol.Request{}, ErrDenied
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", execprotocol.Request{}, err
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: work.ID, AgentID: work.Scope.AgentID, AuthorizationVersion: binding.AuthorizationVersion, Kind: kind, Arguments: data}
	return binding.EnvironmentID, request, request.Validate()
}
