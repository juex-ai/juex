package platformrpc

import (
	"context"
	"encoding/json"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (h *managementHandler) ExtensionCatalog(ctx context.Context, scopeJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if len(scopeJSON) > 4096 || json.Unmarshal([]byte(scopeJSON), &scope) != nil {
		return invalid()
	}
	authority, ok := h.authority.(managedruntime.ExtensionAuthority)
	if !ok {
		return invalid()
	}
	value, err := authority.ExtensionCatalog(ctx, scope)
	return reply(value, err)
}
func (h *runtimeHandler) ObservationSources(ctx context.Context, actor *platform.Actor, after string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if !validActor(actor) {
		return invalid()
	}
	value, err := h.service.ObservationSources(ctx, actor.UserID, actor.TenantID, actor.AgentID, after)
	return reply(value, err)
}
func (h *runtimeHandler) ObservedEvents(ctx context.Context, actor *platform.Actor, source, after string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if !validActor(actor) {
		return invalid()
	}
	value, err := h.service.ObservedEvents(ctx, actor.UserID, actor.TenantID, actor.AgentID, source, after)
	return reply(value, err)
}
func (h *runtimeHandler) ObservationContent(ctx context.Context, actor *platform.Actor, id string, offset, limit int32) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if !validActor(actor) {
		return invalid()
	}
	value, err := h.service.ObservationContent(ctx, actor.UserID, actor.TenantID, actor.AgentID, id, int(offset), int(limit))
	return reply(value, err)
}
func (h *runtimeHandler) StartObserver(ctx context.Context, actor *platform.Actor, requestJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var request managedruntime.ObserverStart
	if !validActor(actor) || len(requestJSON) > 8192 || !appDecode(requestJSON, &request) {
		return invalid()
	}
	value, err := h.service.StartObserver(ctx, actor.UserID, actor.TenantID, actor.AgentID, request)
	return reply(value, err)
}
func (h *runtimeHandler) StopObserver(ctx context.Context, actor *platform.Actor, source string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if !validActor(actor) {
		return invalid()
	}
	return reply(nil, h.service.StopObserver(ctx, actor.UserID, actor.TenantID, actor.AgentID, source))
}
func (h *runtimeHandler) SetSourceSubscription(ctx context.Context, actor *platform.Actor, source, thread string, enabled bool) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if !validActor(actor) {
		return invalid()
	}
	value, err := h.service.SetSourceSubscription(ctx, actor.UserID, actor.TenantID, actor.AgentID, source, thread, enabled)
	return reply(value, err)
}
