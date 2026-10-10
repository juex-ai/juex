package platformrpc

import (
	"context"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
)

type agentController interface {
	ControlAgents(context.Context, agentcontrol.Source, string, string) (agentcontrol.Page, error)
	ControlAgent(context.Context, agentcontrol.Source, agentcontrol.Action, bool) (agentcontrol.Receipt, error)
}

func (h *managementHandler) ControlAgents(ctx context.Context, sourceJSON, agentID, after string) (*platform.Reply, error) {
	controller, ok := h.authority.(agentController)
	if transport.CallerRole(ctx) != "runtime" || !ok {
		return reply(nil, managedruntime.ErrDenied)
	}
	var source agentcontrol.Source
	if len(sourceJSON) > 4096 || !appDecode(sourceJSON, &source) || !source.Valid() {
		return invalid()
	}
	value, err := controller.ControlAgents(ctx, source, agentID, after)
	return reply(value, err)
}
func (h *managementHandler) ControlAgent(ctx context.Context, sourceJSON, actionJSON string, cancel bool) (*platform.Reply, error) {
	controller, ok := h.authority.(agentController)
	if transport.CallerRole(ctx) != "runtime" || !ok {
		return reply(nil, managedruntime.ErrDenied)
	}
	var source agentcontrol.Source
	var action agentcontrol.Action
	if len(sourceJSON) > 4096 || len(actionJSON) > 128<<10 || !appDecode(sourceJSON, &source) || !source.Valid() || !appDecode(actionJSON, &action) {
		return invalid()
	}
	value, err := controller.ControlAgent(ctx, source, action, cancel)
	return reply(value, err)
}
