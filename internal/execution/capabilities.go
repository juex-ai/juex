package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func operationAllowed(policy agentpolicy.Policy, request execprotocol.Request) bool {
	if !policy.Allows(agentpolicy.Capability(execprotocol.RequiredCapability(request.Kind))) {
		return false
	}
	switch request.Kind {
	case "run_hook":
		if !policy.Allows(agentpolicy.Hooks) {
			return false
		}
	case "observe_command":
		if !policy.Allows(agentpolicy.Observations) {
			return false
		}
	case "inspect_extension":
		if !policy.Allows(agentpolicy.Extensions) {
			return false
		}
	}
	if len(request.Arguments) > 0 {
		var args struct {
			Extension json.RawMessage `json:"extension"`
		}
		if json.Unmarshal(request.Arguments, &args) != nil {
			return false
		}
		if len(args.Extension) > 0 && !bytes.Equal(bytes.TrimSpace(args.Extension), []byte("null")) && !policy.Allows(agentpolicy.Extensions) {
			return false
		}
	}
	return true
}

func permittedCapabilities(policy agentpolicy.Policy, capabilities []execprotocol.Capability) []execprotocol.Capability {
	return slices.DeleteFunc(slices.Clone(capabilities), func(capability execprotocol.Capability) bool {
		return !policy.Allows(agentpolicy.Capability(capability))
	})
}

// Retained process/connection IDs never authorize a new action. Check the
// original operation's authority before accepting work against that handle.
func (s *Service) authorizeHandle(ctx context.Context, scope Scope, environment string, request execprotocol.Request) error {
	var args struct {
		OperationID  string `json:"operation_id"`
		ConnectionID string `json:"connection_id"`
	}
	switch request.Kind {
	case "write_stdin", "mcp_call", "mcp_list":
	default:
		return nil
	}
	if json.Unmarshal(request.Arguments, &args) != nil {
		return execprotocol.ErrInvalid
	}
	id := args.ConnectionID
	if request.Kind == "write_stdin" {
		id = args.OperationID
	}
	if id == "" {
		return execprotocol.ErrInvalid
	}
	original, err := s.Store.Operation(ctx, environment, id, 0, 1)
	if err != nil {
		return err
	}
	validKind := original.Request.Kind == "mcp_connect"
	if request.Kind == "write_stdin" {
		validKind = original.Request.Kind == "exec_command" || original.Request.Kind == "observe_command"
	}
	if !validKind || original.CancelRequested || !scope.SameAuthority(original.Scope) || !operationAllowed(scope.Capabilities, original.Request) {
		return execprotocol.ErrDenied
	}
	return nil
}
