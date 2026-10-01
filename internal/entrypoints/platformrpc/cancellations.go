package platformrpc

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
)

func (h *executionHandler) CancelPreparedOperation(ctx context.Context, actor *platform.Actor, environment, request string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	state, err := h.service.CancelPreparedOperation(ctx, actor.UserID, actor.TenantID, actor.AgentID, environment, request)
	return executionReply(state, err)
}

func (h *executionHandler) CancelPreparedTransfer(ctx context.Context, actor *platform.Actor, request string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	state, err := h.service.CancelPreparedTransfer(ctx, actor.UserID, actor.TenantID, actor.AgentID, request)
	return executionReply(state, err)
}
