package platformrpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
)

func (h *executionHandler) BeginTransfer(ctx context.Context, actor *platform.Actor, encoded string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	var request execution.TransferRequest
	if !validActor(actor) || len(encoded) > 16<<10 || json.Unmarshal([]byte(encoded), &request) != nil {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.BeginTransfer(ctx, actor.UserID, actor.TenantID, actor.AgentID, request)
	return executionReply(v, err)
}

func (h *executionHandler) BeginTransferFenced(ctx context.Context, actor *platform.Actor, encoded, encodedFence string) (*platform.Reply, error) {
	var request execution.TransferRequest
	var fence execprotocol.AuthorityFence
	if !validActor(actor) || len(encoded) > 16<<10 || len(encodedFence) > 1024 || json.Unmarshal([]byte(encoded), &request) != nil || json.Unmarshal([]byte(encodedFence), &fence) != nil {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.BeginTransferFenced(ctx, actor.UserID, actor.TenantID, actor.AgentID, request, fence)
	return executionReply(v, err)
}

func (h *executionHandler) Transfer(ctx context.Context, actor *platform.Actor, id string) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.Transfer(ctx, actor.UserID, actor.TenantID, actor.AgentID, id)
	return executionReply(v, err)
}

func (h *executionHandler) ListTransfers(ctx context.Context, actor *platform.Actor, after string, limit int32) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.ListTransfers(ctx, actor.UserID, actor.TenantID, actor.AgentID, after, int(limit))
	return executionReply(v, err)
}

func (h *executionHandler) CancelTransfer(ctx context.Context, actor *platform.Actor, id string) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	return executionReply(nil, h.service.CancelTransfer(ctx, actor.UserID, actor.TenantID, actor.AgentID, id))
}

func (h *executionHandler) ExtendTransfer(ctx context.Context, actor *platform.Actor, id string, wait int64) (*platform.Reply, error) {
	if !validActor(actor) || wait < time.Minute.Milliseconds() || wait > (30*24*time.Hour).Milliseconds() {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	return executionReply(nil, h.service.ExtendTransfer(ctx, actor.UserID, actor.TenantID, actor.AgentID, id, time.Duration(wait)*time.Millisecond))
}
