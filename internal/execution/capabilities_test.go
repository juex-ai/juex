package execution

import (
	"encoding/json"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestAgentPolicyConstrainsDirectExecutionRequests(t *testing.T) {
	for _, test := range []struct {
		capability agentpolicy.Capability
		kind       string
		arguments  string
	}{
		{agentpolicy.Files, "read", `{}`},
		{agentpolicy.Shell, "exec_command", `{}`},
		{agentpolicy.MCP, "mcp_connect", `{}`},
		{agentpolicy.Hooks, "run_hook", `{}`},
		{agentpolicy.Observations, "observe_command", `{}`},
		{agentpolicy.Extensions, "inspect_extension", `{}`},
		{agentpolicy.Extensions, "exec_command", `{"extension":{"binding_id":"installed"}}`},
		{agentpolicy.Extensions, "mcp_connect", `{"extension":{"binding_id":"installed"}}`},
		{agentpolicy.Extensions, "observe_command", `{"extension":{"binding_id":"installed"}}`},
		{agentpolicy.Extensions, "run_hook", `{"extension":{"binding_id":"installed"}}`},
	} {
		t.Run(test.kind+"/"+string(test.capability), func(t *testing.T) {
			request := execprotocol.Request{Kind: test.kind, Arguments: json.RawMessage(test.arguments)}
			policy := agentpolicy.Policy{Disabled: []agentpolicy.Capability{test.capability}}
			if operationAllowed(policy, request) || !operationAllowed(agentpolicy.Policy{}, request) {
				t.Fatal("request ignored Agent policy", request)
			}
		})
	}
}
