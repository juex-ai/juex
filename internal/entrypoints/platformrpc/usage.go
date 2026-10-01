package platformrpc

import (
	"context"

	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (h *managementHandler) AuthorizeUsage(ctx context.Context, actor, tenant, owner string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return reply(nil, managedruntime.ErrDenied)
	}
	if actor == "" || tenant == "" {
		return invalid()
	}
	a, ok := h.authority.(managedruntime.UsageAuthority)
	if !ok {
		return reply(nil, managedruntime.ErrDenied)
	}
	return reply(nil, a.AuthorizeUsage(ctx, actor, tenant, owner))
}

func (h *runtimeHandler) Usage(ctx context.Context, actor, queryJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var query managedruntime.UsageQuery
	if actor == "" || len(queryJSON) > 4096 || !appDecode(queryJSON, &query) {
		return invalid()
	}
	v, err := h.service.Usage(ctx, actor, query)
	return reply(v, err)
}

func (h *runtimeHandler) OperatorUsage(ctx context.Context, queryJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var query managedruntime.UsageQuery
	if len(queryJSON) > 4096 || !appDecode(queryJSON, &query) {
		return invalid()
	}
	v, err := h.service.OperatorUsage(ctx, query)
	return reply(v, err)
}

func (h *runtimeHandler) ConfigureUsage(ctx context.Context, policyJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var policy managedruntime.UsagePolicy
	if len(policyJSON) > 4096 || !appDecode(policyJSON, &policy) {
		return invalid()
	}
	v, err := h.service.ConfigureUsage(ctx, policy)
	return reply(v, err)
}
