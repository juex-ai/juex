package platformrpc

import (
	"context"
	"encoding/json"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func (h *managementHandler) ResolveProcessEnvironment(ctx context.Context, raw string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "execution" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var access management.ProcessEnvironmentAccess
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &access) != nil {
		return invalid()
	}
	resolver, ok := h.authority.(interface {
		ResolveProcessEnvironment(context.Context, management.ProcessEnvironmentAccess) (map[string]string, error)
	})
	if !ok {
		return reply(nil, managedruntime.ErrDenied)
	}
	values, err := resolver.ResolveProcessEnvironment(ctx, access)
	return reply(values, err)
}
