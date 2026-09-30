package platformrpc

import (
	"context"

	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func applicationCaller(ctx context.Context, application string) bool {
	return (application == "memory" || application == "calendar") && transport.CallerRole(ctx) == application
}

func (h *runtimeHandler) AdmitApplication(ctx context.Context, scopeJSON, jobJSON string) (*platform.Reply, error) {
	var scope managedruntime.Scope
	var job managedruntime.ApplicationJob
	if !appDecode(scopeJSON, &scope) || !appDecode(jobJSON, &job) {
		return invalid()
	}
	if !applicationCaller(ctx, job.Application) {
		return reply(nil, managedruntime.ErrDenied)
	}
	value, err := h.service.AdmitApplication(ctx, scope, job)
	return reply(value, err)
}

func (h *runtimeHandler) ApplicationReceipt(ctx context.Context, scopeJSON, application, id string) (*platform.Reply, error) {
	if !applicationCaller(ctx, application) {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if !appDecode(scopeJSON, &scope) {
		return invalid()
	}
	value, err := h.service.ApplicationReceipt(ctx, scope, application, id)
	return reply(value, err)
}

func (h *runtimeHandler) CancelApplication(ctx context.Context, scopeJSON, application, id string) (*platform.Reply, error) {
	if !applicationCaller(ctx, application) {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if !appDecode(scopeJSON, &scope) {
		return invalid()
	}
	return reply(nil, h.service.CancelApplication(ctx, scope, application, id))
}
