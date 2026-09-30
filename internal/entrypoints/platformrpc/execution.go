package platformrpc

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/cloudwego/kitex/server"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	executionwire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/execution"
)

func NewExecution(listener net.Listener, credentials transport.Credentials, service *execution.Service, health func(context.Context) error) (server.Server, error) {
	opts, err := transport.ServerOptions(listener, credentials, "management", "runtime")
	if err != nil {
		return nil, err
	}
	return newLifecycleServer(func(options ...server.Option) server.Server {
		return executionwire.NewServer(&executionHandler{service: service, health: health}, options...)
	}, opts), nil
}

type executionHandler struct {
	service *execution.Service
	health  func(context.Context) error
}

func executionReply(value any, err error) (*platform.Reply, error) {
	return transport.Reply(value, execprotocol.ErrorCode(err)), nil
}
func managementCaller(ctx context.Context) bool { return transport.CallerRole(ctx) == "management" }
func (h *executionHandler) Health(ctx context.Context) (*platform.Reply, error) {
	return executionReply(map[string]int{"protocol_version": execprotocol.Version}, h.health(ctx))
}
func (h *executionHandler) PreviewPair(ctx context.Context, actor, tenant, pair string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	v, err := h.service.PreviewPair(ctx, actor, tenant, pair)
	return executionReply(v, err)
}
func decodeGrants(encoded string) (map[string][]execprotocol.Capability, error) {
	var grants map[string][]execprotocol.Capability
	if len(encoded) > 64<<10 || json.Unmarshal([]byte(encoded), &grants) != nil {
		return nil, execprotocol.ErrInvalid
	}
	return grants, nil
}
func (h *executionHandler) ApprovePair(ctx context.Context, actor, tenant, pair, encoded string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	grants, err := decodeGrants(encoded)
	if err != nil {
		return executionReply(nil, err)
	}
	v, err := h.service.ApprovePair(ctx, actor, tenant, pair, grants)
	return executionReply(v, err)
}
func (h *executionHandler) Devices(ctx context.Context, actor, tenant, owner string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	v, err := h.service.Devices(ctx, actor, tenant, owner)
	return executionReply(v, err)
}
func (h *executionHandler) Restrict(ctx context.Context, actor, tenant, environment string, version int64, encoded string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	grants, err := decodeGrants(encoded)
	if err != nil {
		return executionReply(nil, err)
	}
	v, err := h.service.Restrict(ctx, actor, tenant, environment, version, grants)
	return executionReply(v, err)
}
func (h *executionHandler) Revoke(ctx context.Context, actor, tenant, environment string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	return executionReply(nil, h.service.Revoke(ctx, actor, tenant, environment))
}
func (h *executionHandler) Environments(ctx context.Context, actor *platform.Actor) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.Environments(ctx, actor.UserID, actor.TenantID, actor.AgentID)
	return executionReply(v, err)
}
func (h *executionHandler) Submit(ctx context.Context, actor *platform.Actor, environment, encoded string, wait int64) (*platform.Reply, error) {
	var request execprotocol.Request
	if !validActor(actor) || len(encoded) > 3<<20 || json.Unmarshal([]byte(encoded), &request) != nil || request.AgentID != actor.AgentID || wait < 0 || wait > (30*24*time.Hour).Milliseconds() {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.Submit(ctx, actor.UserID, actor.TenantID, environment, request, time.Duration(wait)*time.Millisecond)
	return executionReply(v, err)
}
func (h *executionHandler) Operation(ctx context.Context, actor *platform.Actor, environment, id string, cursor int64, limit int32) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.Operation(ctx, actor.UserID, actor.TenantID, actor.AgentID, environment, id, cursor, int(limit))
	return executionReply(v, err)
}
func (h *executionHandler) Cancel(ctx context.Context, actor *platform.Actor, environment, id string) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	return executionReply(nil, h.service.Cancel(ctx, actor.UserID, actor.TenantID, actor.AgentID, environment, id))
}
func (h *executionHandler) Extend(ctx context.Context, actor *platform.Actor, environment, id string, wait int64) (*platform.Reply, error) {
	if !validActor(actor) || wait < 0 || wait > (30*24*time.Hour).Milliseconds() {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	return executionReply(nil, h.service.Extend(ctx, actor.UserID, actor.TenantID, actor.AgentID, environment, id, time.Duration(wait)*time.Millisecond))
}
