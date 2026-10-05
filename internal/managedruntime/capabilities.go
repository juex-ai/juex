package managedruntime

import (
	"strings"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
)

// Catalog and dispatch share this map so a fabricated model call cannot regain
// a capability hidden from the frozen Turn's tool declarations.
func toolAllowed(policy agentpolicy.Policy, name string) bool {
	switch {
	case strings.HasPrefix(name, "memory_"):
		return policy.Allows(agentpolicy.Memory)
	case strings.HasPrefix(name, "calendar_"):
		return policy.Allows(agentpolicy.Calendar)
	case strings.HasPrefix(name, "thread_"):
		return policy.Allows(agentpolicy.Workers)
	}
	switch name {
	case "read_context":
		return true
	case "agent_list", "agent_send":
		return policy.Allows(agentpolicy.Collaboration)
	case "read", "write", "edit", "glob", "grep", "publish_file", "import_file", "copy_file", "list_artifacts", "file_transfer_status", "file_transfer_cancel":
		return policy.Allows(agentpolicy.Files)
	case "exec_command", "write_stdin", "process_status", "process_cancel":
		return policy.Allows(agentpolicy.Shell)
	case "mcp_connect", "mcp_list", "mcp_call", "mcp_close":
		return policy.Allows(agentpolicy.MCP)
	case "subscribe", "unsubscribe", "list_subscriptions", "read_observation":
		return policy.Allows(agentpolicy.Observations)
	case "skill_search", "skill_load":
		return policy.Allows(agentpolicy.Extensions)
	case "extension_exec":
		return policy.Allows(agentpolicy.Extensions) && policy.Allows(agentpolicy.Shell)
	case "extension_mcp_connect":
		return policy.Allows(agentpolicy.Extensions) && policy.Allows(agentpolicy.MCP)
	case "extension_observe":
		return policy.Allows(agentpolicy.Extensions) && policy.Allows(agentpolicy.Shell) && policy.Allows(agentpolicy.Observations)
	default:
		return false
	}
}

func usesExecution(policy agentpolicy.Policy) bool {
	return policy.Allows(agentpolicy.Files) || policy.Allows(agentpolicy.Shell) || policy.Allows(agentpolicy.MCP)
}
