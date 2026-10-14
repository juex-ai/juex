package execution

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestRetainedHandleRequiresOriginalAuthority(t *testing.T) {
	scope := Scope{AgentID: "agent", AgentExecutionEpoch: 1}
	request := execprotocol.Request{Kind: "write_stdin", Arguments: json.RawMessage(`{"operation_id":"process"}`)}
	for _, test := range []struct {
		name   string
		change func(*Operation)
	}{
		{"cancelled", func(op *Operation) { op.CancelRequested = true }},
		{"different-agent", func(op *Operation) { op.Scope.AgentID = "other" }},
		{"old-authority", func(op *Operation) { op.Scope.AgentExecutionEpoch = 0 }},
		{"wrong-kind", func(op *Operation) { op.Request.Kind = "mcp_connect" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := Operation{Scope: scope, Request: execprotocol.Request{Kind: "exec_command", Arguments: json.RawMessage(`{}`)}}
			lookup := func(id string) (Operation, error) {
				if id != "process" {
					t.Fatal("wrong handle", id)
				}
				return original, nil
			}
			if err := AuthorizeHandle(scope, request, lookup); err != nil {
				t.Fatal("current handle rejected", err)
			}
			test.change(&original)
			if err := AuthorizeHandle(scope, request, lookup); !errors.Is(err, execprotocol.ErrDenied) {
				t.Fatal("retained handle expanded authority", err)
			}
		})
	}
}

func TestIndependentSearchAndSkillSourceAuthority(t *testing.T) {
	searchOnly := agentpolicy.Policy{Version: 1, Disabled: []agentpolicy.Capability{agentpolicy.Files, agentpolicy.Extensions, agentpolicy.Skills}}
	for _, kind := range []string{"glob", "grep"} {
		if !operationAllowed(searchOnly, execprotocol.Request{Kind: kind, Arguments: json.RawMessage(`{}`)}) {
			t.Fatal("search-only denied", kind)
		}
	}
	for _, kind := range []string{"read", "write", "inspect_extension"} {
		if operationAllowed(searchOnly, execprotocol.Request{Kind: kind, Arguments: json.RawMessage(`{}`)}) {
			t.Fatal("search granted broader files", kind)
		}
	}
	if got := permittedCapabilities(searchOnly, []execprotocol.Capability{execprotocol.Files}); len(got) != 1 {
		t.Fatal("device lost the selected search implementation")
	}
	request := execprotocol.Request{Kind: "inspect_extension", Arguments: json.RawMessage(`{"source_kind":"skills"}`)}
	inherited := agentpolicy.Policy{Version: 1, Disabled: []agentpolicy.Capability{agentpolicy.Files}}
	if operationAllowed(inherited, request) {
		t.Fatal("inherited extension skills granted a new directory read")
	}
	independent := agentpolicy.Policy{Version: 1, SkillSources: true, Disabled: []agentpolicy.Capability{agentpolicy.Files, agentpolicy.Extensions}}
	if !operationAllowed(independent, request) || !inherited.Restricts(independent) {
		t.Fatal("explicit skills source not granted/revoked")
	}
	request.Arguments = json.RawMessage(`{"source_kind":"skills","extension":{"binding_id":"forged"}}`)
	if operationAllowed(independent, request) {
		t.Fatal("source inspection accepted extension process context")
	}
}

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

func TestPatchRequiresBothFilesAndExplicitModule(t *testing.T) {
	request := execprotocol.Request{Kind: "apply_patch", Arguments: json.RawMessage(`{}`)}
	if operationAllowed(agentpolicy.Policy{}, request) {
		t.Fatal("optional tool enabled by default")
	}
	policy := agentpolicy.Policy{Enabled: []agentpolicy.Capability{agentpolicy.ApplyPatch}}
	if !operationAllowed(policy, request) {
		t.Fatal("explicit patch denied")
	}
	policy.Disabled = []agentpolicy.Capability{agentpolicy.Files}
	if operationAllowed(policy, request) {
		t.Fatal("patch bypassed file authority")
	}
}
