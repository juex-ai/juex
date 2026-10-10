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
	capability := agentpolicy.Capability(execprotocol.RequiredCapability(request.Kind))
	if request.Kind == "glob" || request.Kind == "grep" {
		capability = agentpolicy.FileSearch
	}
	var args struct {
		Extension  json.RawMessage `json:"extension"`
		SourceKind string          `json:"source_kind"`
	}
	if len(request.Arguments) > 0 && json.Unmarshal(request.Arguments, &args) != nil {
		return false
	}
	if request.Kind == "inspect_extension" && args.SourceKind == "skills" {
		if !policy.CanInspectSkills() || len(args.Extension) > 0 && !bytes.Equal(bytes.TrimSpace(args.Extension), []byte("null")) {
			return false
		}
		return true
	}
	if !policy.Allows(capability) {
		return false
	}
	switch request.Kind {
	case "write_begin", "write_chunk", "write_commit", "write_abort":
		if !policy.Allows(agentpolicy.ChunkedWrite) {
			return false
		}
	case "apply_patch":
		if !policy.Allows(agentpolicy.ApplyPatch) {
			return false
		}
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
		if len(args.Extension) > 0 && !bytes.Equal(bytes.TrimSpace(args.Extension), []byte("null")) && !policy.Allows(agentpolicy.Extensions) {
			return false
		}
	}
	return true
}

func permittedCapabilities(policy agentpolicy.Policy, capabilities []execprotocol.Capability) []execprotocol.Capability {
	return slices.DeleteFunc(slices.Clone(capabilities), func(capability execprotocol.Capability) bool {
		if capability == execprotocol.Files {
			return !policy.Allows(agentpolicy.Files) && !policy.Allows(agentpolicy.FileSearch) && !policy.CanInspectSkills()
		}
		return !policy.Allows(agentpolicy.Capability(capability))
	})
}

// Retained process/connection IDs never authorize a new action. Check the
// original operation's authority before accepting work against that handle.
func (s *Service) authorizeHandle(ctx context.Context, scope Scope, environment string, request execprotocol.Request) error {
	var args struct {
		OperationID  string `json:"operation_id"`
		ConnectionID string `json:"connection_id"`
		WriteID      string `json:"write_id"`
	}
	switch request.Kind {
	case "write_stdin", "mcp_call", "mcp_list", "write_chunk", "write_commit", "write_abort":
	default:
		return nil
	}
	if json.Unmarshal(request.Arguments, &args) != nil {
		return execprotocol.ErrInvalid
	}
	id := args.ConnectionID
	if execprotocol.IsChunkedWrite(request.Kind) {
		id = args.WriteID
	}
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
	if execprotocol.IsChunkedWrite(request.Kind) {
		validKind = original.Request.Kind == "write_begin" && original.Request.WriteContext != nil && request.WriteContext != nil && *original.Request.WriteContext == *request.WriteContext && original.Request.AuthorizationVersion == request.AuthorizationVersion
	}
	if request.Kind == "write_stdin" {
		validKind = original.Request.Kind == "exec_command" || original.Request.Kind == "observe_command"
	}
	if !validKind || original.CancelRequested || !scope.SameAuthority(original.Scope) || !operationAllowed(scope.Capabilities, original.Request) {
		return execprotocol.ErrDenied
	}
	return nil
}
