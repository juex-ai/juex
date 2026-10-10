package platformrpc

import (
	"context"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (h *runtimeHandler) AgentRunState(ctx context.Context, actor *platform.Actor) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if !validActor(actor) {
		return invalid()
	}
	value, err := h.service.AgentRunState(ctx, actor.UserID, actor.TenantID, actor.AgentID)
	return reply(value, err)
}
func (h *runtimeHandler) ChangeAgentLifecycle(ctx context.Context, actor *platform.Actor, requestJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var change managedruntime.AgentLifecycleChange
	if !validActor(actor) || len(requestJSON) > 8192 || !appDecode(requestJSON, &change) {
		return invalid()
	}
	value, err := h.service.ChangeAgentLifecycle(ctx, actor.UserID, actor.TenantID, actor.AgentID, change)
	return reply(value, err)
}
