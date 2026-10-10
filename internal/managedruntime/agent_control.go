package managedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type AgentController interface {
	ControlAgents(context.Context, agentcontrol.Source, string, string) (agentcontrol.Page, error)
	ControlAgent(context.Context, agentcontrol.Source, agentcontrol.Action, bool) (agentcontrol.Receipt, error)
}
type AgentControlStore interface {
	CheckAgentControl(context.Context, ToolWork) error
	PrepareAgentControl(context.Context, ToolWork, agentcontrol.Action) error
	ControlAgentLifecycle(context.Context, ToolWork, Scope, AgentLifecycleChange) (AgentLifecycleReceipt, error)
}

func isAgentControlTool(name string) bool {
	switch name {
	case "fleet_agents", "fleet_agent_config", "fleet_agent_create", "fleet_agent_configure", "fleet_agent_lifecycle":
		return true
	}
	return false
}

func agentControlSource(work ToolWork) agentcontrol.Source {
	s := work.Scope
	return agentcontrol.Source{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, FleetID: s.FleetID, AgentID: s.AgentID, ThreadID: work.ThreadID, ActionID: work.ID, ActorEpoch: s.ActorAuthorizationEpoch, MembershipEpoch: s.MembershipExecutionEpoch, AgentEpoch: s.AgentExecutionEpoch}
}

func agentControlTools() []llm.ToolSpec {
	str := map[string]any{"type": "string"}
	version := map[string]any{"type": "integer", "minimum": 1}
	tool := func(name, description string, props map[string]any, required ...string) llm.ToolSpec {
		if required == nil {
			required = []string{}
		}
		return llm.ToolSpec{Name: name, Description: description, Schema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}
	}
	modules := map[string]any{}
	for _, capability := range agentpolicy.Capabilities() {
		modules[string(capability)] = map[string]any{"type": []string{"boolean", "null"}}
	}
	patch := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"name": str, "instructions": str, "worker_depth": map[string]any{"type": "integer", "minimum": 1, "maximum": 2}, "models": map[string]any{"type": "array", "maxItems": 5, "items": str, "description": "Ordered model IDs; an empty array inherits lower layers."}, "module_preset": map[string]any{"type": "string", "enum": []string{"", "standard", "minimal"}}, "modules": map[string]any{"type": "object", "properties": modules, "additionalProperties": false, "description": "Omitted keys are unchanged; null removes an Agent override."},
		"dynamic_instructions": map[string]any{"type": "object", "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}, "global_path": str}, "required": []string{"enabled", "global_path"}, "additionalProperties": false},
	}}
	return []llm.ToolSpec{
		tool("fleet_agents", "List other Agents in your Fleet, with IDs and configuration versions. No conversations or credentials. Paginate with next as after.", map[string]any{"reason": str, "after": str}, "reason"),
		tool("fleet_agent_config", "Read another Agent's safe configuration declaration and version. Hooks, credentials and conversations are private.", map[string]any{"agent_id": str}, "agent_id"),
		tool("fleet_agent_create", "Create another Agent in this Fleet. This is an independent persistent identity. Cannot grant Agent management authority.", map[string]any{"patch": patch}, "patch"),
		tool("fleet_agent_configure", "Apply a typed patch to another Agent using its current version. Omitted fields, Hooks and environment values are preserved. New settings apply to future Turns.", map[string]any{"agent_id": str, "version": version, "patch": patch}, "agent_id", "version", "patch"),
		tool("fleet_agent_lifecycle", "Read, pause, resume, or rebuild another Agent's processing lease. Busy pause/restart defers unless interrupt is explicit. Deferred requests require a new request; accepted tools keep settling. Never restarts shared services.", map[string]any{"agent_id": str, "action": map[string]any{"type": "string", "enum": []string{"status", "pause", "resume", "restart"}}, "version": version, "interrupt": map[string]any{"type": "boolean"}}, "agent_id", "action"),
	}
}

func (r toolRunner) recoverAgentControl(ctx context.Context, work ToolWork) ToolOutcome {
	controller, ok := r.authority.(AgentController)
	if !ok {
		return retryTool()
	}
	value, err := controller.ControlAgent(ctx, agentControlSource(work), *work.AgentControl, work.Cancelled)
	if err != nil {
		return agentControlError(work, err)
	}
	return toolResult(work.Call, value, value.Outcome == "rejected")
}

func agentControlError(work ToolWork, err error) ToolOutcome {
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied) || errors.Is(err, ErrConflict) {
		return toolResult(work.Call, map[string]string{"error": err.Error()}, true)
	}
	return retryTool()
}

func (r toolRunner) agentControlTool(ctx context.Context, work *ToolWork) ToolOutcome {
	controller, ok := r.authority.(AgentController)
	if !ok || r.agentControl == nil || !work.Scope.AgentManagement || !work.FrozenAgentManagement {
		return agentControlError(*work, ErrDenied)
	}
	for _, spec := range agentControlTools() {
		if spec.Name != work.Call.ToolName {
			continue
		}
		properties := spec.Schema["properties"].(map[string]any)
		for key := range work.Call.Input {
			if _, ok := properties[key]; !ok {
				return agentControlError(*work, ErrInvalid)
			}
		}
	}
	var args struct {
		Reason    string              `json:"reason"`
		After     string              `json:"after"`
		AgentID   string              `json:"agent_id"`
		Version   int64               `json:"version"`
		Patch     *agentcontrol.Patch `json:"patch"`
		Action    string              `json:"action"`
		Interrupt bool                `json:"interrupt"`
	}
	encoded, err := json.Marshal(work.Call.Input)
	if err != nil || len(encoded) > 128<<10 {
		return agentControlError(*work, ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil {
		return agentControlError(*work, ErrInvalid)
	}
	if err = r.agentControl.CheckAgentControl(ctx, *work); err != nil {
		return agentControlError(*work, err)
	}
	source := agentControlSource(*work)
	switch work.Call.ToolName {
	case "fleet_agents", "fleet_agent_config":
		if work.Call.ToolName == "fleet_agents" && (args.Reason == "" || len(args.Reason) > 512 || args.AgentID != "") || work.Call.ToolName == "fleet_agent_config" && args.AgentID == "" {
			return agentControlError(*work, ErrInvalid)
		}
		value, err := controller.ControlAgents(ctx, source, args.AgentID, args.After)
		if err != nil {
			return agentControlError(*work, err)
		}
		return toolResult(work.Call, value, false)
	case "fleet_agent_create", "fleet_agent_configure":
		kind := "create"
		if work.Call.ToolName == "fleet_agent_configure" {
			kind = "configure"
		}
		action := agentcontrol.Action{Kind: kind, AgentID: args.AgentID, Version: args.Version, Patch: args.Patch}
		if !action.Valid() {
			return agentControlError(*work, ErrInvalid)
		}
		if err := r.agentControl.PrepareAgentControl(ctx, *work, action); err != nil {
			return agentControlError(*work, err)
		}
		work.AgentControl = &action
		return r.recoverAgentControl(ctx, *work)
	case "fleet_agent_lifecycle":
		target, err := r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, args.AgentID, true)
		if err != nil {
			return agentControlError(*work, err)
		}
		if args.AgentID == work.Scope.AgentID || target.FleetID != work.Scope.FleetID || target.UserID != work.Scope.UserID || target.TenantID != work.Scope.TenantID {
			return agentControlError(*work, ErrDenied)
		}
		if args.Action == "status" {
			store, ok := r.store.(LifecycleStore)
			if !ok {
				return agentControlError(*work, ErrDenied)
			}
			value, err := store.AgentRunState(ctx, target)
			if err != nil {
				return agentControlError(*work, err)
			}
			return toolResult(work.Call, value, false)
		}
		value, err := r.agentControl.ControlAgentLifecycle(ctx, *work, target, AgentLifecycleChange{RequestID: work.ID, Action: args.Action, Version: args.Version, Interrupt: args.Interrupt})
		if err != nil {
			return agentControlError(*work, err)
		}
		return toolResult(work.Call, value, false)
	}
	return agentControlError(*work, ErrInvalid)
}
