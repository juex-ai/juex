package platformrpc

import (
	"context"

	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (h *runtimeHandler) AdmitMainTrigger(ctx context.Context, scopeJSON, triggerJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "calendar" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	var q managedruntime.MainTrigger
	if !appDecode(scopeJSON, &scope) || !appDecode(triggerJSON, &q) {
		return invalid()
	}
	value, err := h.service.AdmitMainTrigger(ctx, scope, q)
	return reply(value, err)
}
func (h *runtimeHandler) MainTriggerReceipt(ctx context.Context, scopeJSON, id string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "calendar" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if !appDecode(scopeJSON, &scope) {
		return invalid()
	}
	value, err := h.service.MainTriggerReceipt(ctx, scope, id)
	return reply(value, err)
}
func (h *runtimeHandler) CancelMainTrigger(ctx context.Context, scopeJSON, id string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "calendar" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if !appDecode(scopeJSON, &scope) {
		return invalid()
	}
	value, err := h.service.CancelMainTrigger(ctx, scope, id)
	return reply(value, err)
}
