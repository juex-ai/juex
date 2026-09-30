package managedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (r toolRunner) observationTool(ctx context.Context, work ToolWork) (ToolOutcome, bool) {
	switch work.Call.ToolName {
	case "subscribe", "unsubscribe", "list_subscriptions", "read_observation":
	default:
		return ToolOutcome{}, false
	}
	result, err := r.executeObservationTool(ctx, work)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied) || errors.Is(err, execprotocol.ErrInvalid) || errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) {
			return toolResult(work.Call, map[string]string{"error": err.Error()}, true), true
		}
		return retryTool(), true
	}
	return toolResult(work.Call, result, false), true
}

func (r toolRunner) executeObservationTool(ctx context.Context, work ToolWork) (any, error) {
	encoded, err := json.Marshal(work.Call.Input)
	if err != nil {
		return nil, ErrInvalid
	}
	switch work.Call.ToolName {
	case "list_subscriptions":
		return r.observations.Subscriptions(ctx, work.Scope, work.ThreadID)
	case "unsubscribe":
		id, _ := work.Call.Input["subscription_id"].(string)
		if id == "" {
			return nil, ErrInvalid
		}
		err := r.observations.Unsubscribe(ctx, work, id)
		return map[string]bool{"unsubscribed": err == nil}, err
	case "read_observation":
		var args struct {
			ID     string `json:"observation_id"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if json.Unmarshal(encoded, &args) != nil || args.ID == "" || args.Offset < 0 || args.Limit < 0 || args.Limit > 65536 {
			return nil, ErrInvalid
		}
		if args.Limit == 0 {
			args.Limit = 16384
		}
		fact, err := r.observations.Observation(ctx, work.Scope, args.ID)
		if err != nil {
			return nil, err
		}
		// Rune cursors keep notification text intact across page boundaries.
		data := []rune(string(fact.Data))
		if args.Offset > len(data) {
			return nil, ErrInvalid
		}
		end := min(len(data), args.Offset+args.Limit)
		return map[string]any{"id": fact.ID, "kind": fact.Kind, "data": string(data[args.Offset:end]), "next_offset": end, "total_characters": len(data), "has_more": end < len(data)}, nil
	}
	var request SubscriptionRequest
	if json.Unmarshal(encoded, &request) != nil || request.EnvironmentID == "" || request.ID != "" {
		return nil, ErrInvalid
	}
	if request.Method != "" && !strings.HasPrefix(request.Method, "notifications/") {
		return nil, ErrInvalid
	}
	environments, err := r.gateway.Environments(ctx, work.Scope)
	if err != nil {
		return nil, err
	}
	var environment *execprotocol.Environment
	for i := range environments {
		if environments[i].ID == request.EnvironmentID {
			environment = &environments[i]
			break
		}
	}
	if environment == nil {
		return nil, ErrDenied
	}
	var capability execprotocol.Capability
	var offset int64
	baseline := json.RawMessage(`{}`)
	switch request.Kind {
	case "environment.presence":
		baseline = presence(*environment)
	case "mcp.notification", "operation.terminal":
		if request.OperationID == "" {
			return nil, ErrInvalid
		}
		op, err := r.gateway.Operation(ctx, work.Scope, request.EnvironmentID, request.OperationID, 0)
		if err != nil {
			return nil, err
		}
		capability = execprotocol.RequiredCapability(op.Snapshot.Kind)
		if request.Kind == "mcp.notification" && op.Snapshot.Kind != "mcp_connect" {
			return nil, ErrInvalid
		}
		if op.Snapshot.Kind != "mcp_connect" && op.Snapshot.Kind != "exec_command" {
			return nil, ErrInvalid
		}
		if !slices.Contains(environment.Capabilities, capability) {
			return nil, ErrDenied
		}
		offset = op.Snapshot.OutputBytes
	default:
		return nil, ErrInvalid
	}
	return r.observations.ApplySubscription(ctx, work, request, environment.AuthorizationVersion, offset, capability, baseline)
}
