// Package fleetmanagement adapts the typed Fleet client into Supervisor Main
// Thread tools. It owns no Fleet state or lifecycle policy.
package fleetmanagement

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/fleetclient"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ModuleID = "fleet-management"

type Client interface {
	Agents(context.Context) ([]fleetclient.Agent, error)
	Create(context.Context, fleetclient.CreateRequest) (fleetclient.Result, error)
	Config(context.Context, string) (fleetclient.Config, error)
	Configure(context.Context, string, fleetclient.ConfigRequest) (fleetclient.Result, error)
	Lifecycle(context.Context, string, fleetclient.LifecycleRequest) (fleetclient.Result, error)
}

type Module struct{ client Client }

func New(client Client) *Module      { return &Module{client: client} }
func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	boolean := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	id := text("Target Agent ID. Self mutation is prohibited.")
	interrupt := boolean("Only true when the user explicitly requests interrupting active work. Default false defers busy operations.")
	definition := func(name, description string, properties map[string]any, required []string, run func(context.Context, map[string]any) (any, error)) toolcore.Tool {
		return (toolcore.ToolDefinition{Name: name, Description: description, ExecutionPolicy: toolcore.ToolExecutionSerial, Schema: map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}}).Bind(func(ctx context.Context, input map[string]any) (string, error) {
			if m.client == nil {
				return "", fmt.Errorf("fleet client unavailable")
			}
			result, err := run(ctx, input)
			if err != nil {
				return "", err
			}
			body, err := json.Marshal(result)
			return string(body), err
		})
	}
	str := func(input map[string]any, key string) string { value, _ := input[key].(string); return value }
	flag := func(input map[string]any, key string) bool { value, _ := input[key].(bool); return value }
	return []toolcore.Tool{
		definition("fleet_agents", "List Agents with current runtime health. Does not verify their task behavior.", map[string]any{}, []string{}, func(ctx context.Context, _ map[string]any) (any, error) { return m.client.Agents(ctx) }),
		definition("fleet_agent_create", "Register an Agent in an existing workspace. Reports registration separately from optional startup.", map[string]any{"workspace": text("Existing absolute workspace directory"), "name": text("Display name"), "start": boolean("Start after registration")}, []string{"workspace"}, func(ctx context.Context, input map[string]any) (any, error) {
			return m.client.Create(ctx, fleetclient.CreateRequest{Workspace: str(input, "workspace"), Name: str(input, "name"), Start: flag(input, "start")})
		}),
		definition("fleet_agent_config", "Read the redacted Agent configuration and current revision before editing.", map[string]any{"agent_id": id}, []string{"agent_id"}, func(ctx context.Context, input map[string]any) (any, error) {
			return m.client.Config(ctx, str(input, "agent_id"))
		}),
		definition("fleet_agent_configure", "Validate and publish complete YAML using its expected revision. Preserve redacted secret markers. Saved, applied, restarted, and behavior_verified are distinct. Deferred application requires an explicit retry when idle.", map[string]any{"agent_id": id, "content": text("Complete Agent YAML"), "expected_revision": text("Revision returned by fleet_agent_config"), "apply": boolean("Restart to apply after publishing"), "interrupt": interrupt}, []string{"agent_id", "content", "expected_revision"}, func(ctx context.Context, input map[string]any) (any, error) {
			return m.client.Configure(ctx, str(input, "agent_id"), fleetclient.ConfigRequest{Content: str(input, "content"), ExpectedRevision: str(input, "expected_revision"), Apply: flag(input, "apply"), Interrupt: flag(input, "interrupt")})
		}),
		definition("fleet_agent_lifecycle", "Start, stop, enable, disable, or restart another Agent. Busy stop/disable/restart defers unless explicitly authorized to interrupt. Enable does not start. Runtime readiness does not prove task behavior.", map[string]any{"agent_id": id, "action": map[string]any{"type": "string", "enum": []string{"start", "stop", "enable", "disable", "restart"}}, "interrupt": interrupt}, []string{"agent_id", "action"}, func(ctx context.Context, input map[string]any) (any, error) {
			return m.client.Lifecycle(ctx, str(input, "agent_id"), fleetclient.LifecycleRequest{Action: str(input, "action"), Interrupt: flag(input, "interrupt")})
		}),
	}, nil
}
