package platformrpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
)

func purge(ctx context.Context, encoded string, store any) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "management" {
		return transport.Reply(nil, "denied"), nil
	}
	var request lifecycle.Request
	if len(encoded) > 256<<10 || json.Unmarshal([]byte(encoded), &request) != nil || !request.Valid() {
		return transport.Reply(nil, "invalid"), nil
	}
	participant, ok := store.(lifecycle.Participant)
	if !ok {
		return transport.Reply(nil, "unavailable"), nil
	}
	value, err := participant.Purge(ctx, request)
	if err != nil {
		return transport.Reply(nil, "unavailable"), nil
	}
	return transport.Reply(value, ""), nil
}

func (h *runtimeHandler) Purge(ctx context.Context, encoded string) (*platform.Reply, error) {
	return purge(ctx, encoded, h.service.Store)
}
func (h *memoryHandler) Purge(ctx context.Context, encoded string) (*platform.Reply, error) {
	return purge(ctx, encoded, h.service.Repository)
}
func (h *calendarHandler) Purge(ctx context.Context, encoded string) (*platform.Reply, error) {
	return purge(ctx, encoded, h.service.Repository)
}
func (h *executionHandler) Purge(ctx context.Context, encoded string) (*platform.Reply, error) {
	return purge(ctx, encoded, h.service)
}
