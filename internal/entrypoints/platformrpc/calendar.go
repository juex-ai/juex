package platformrpc

import (
	"context"
	"net"

	"github.com/cloudwego/kitex/server"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	wire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/calendar"
)

func NewCalendar(listener net.Listener, credentials transport.Credentials, service *calendar.Service, health func(context.Context) error) (server.Server, error) {
	options, err := transport.ServerOptions(listener, credentials, "management", "runtime")
	if err != nil {
		return nil, err
	}
	return newLifecycleServer(func(options ...server.Option) server.Server {
		return wire.NewServer(&calendarHandler{service: service, health: health}, options...)
	}, options), nil
}

type calendarHandler struct {
	service *calendar.Service
	health  func(context.Context) error
}

func (h *calendarHandler) Health(ctx context.Context) (*platform.Reply, error) {
	return appReply(map[string]int{"protocol_version": 1}, h.health(ctx))
}
func (h *calendarHandler) Status(ctx context.Context, accessJSON string) (*platform.Reply, error) {
	a, ok := applicationAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	v, err := h.service.Status(ctx, a)
	return appReply(v, err)
}
func (h *calendarHandler) Configure(ctx context.Context, accessJSON string, version int64, enabled bool) (*platform.Reply, error) {
	a, ok := applicationAccess(ctx, accessJSON)
	if !ok || transport.CallerRole(ctx) != "management" {
		return appReply(nil, application.ErrDenied)
	}
	v, err := h.service.Configure(ctx, a, version, enabled)
	return appReply(v, err)
}
func (h *calendarHandler) Schedules(ctx context.Context, accessJSON string, offset, limit int32) (*platform.Reply, error) {
	a, ok := applicationAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	v, err := h.service.Schedules(ctx, a, int(offset), int(limit))
	return appReply(v, err)
}
func (h *calendarHandler) Occurrences(ctx context.Context, accessJSON, scheduleID string, offset, limit int32) (*platform.Reply, error) {
	a, ok := applicationAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	v, err := h.service.Occurrences(ctx, a, scheduleID, int(offset), int(limit))
	return appReply(v, err)
}
func (h *calendarHandler) Change(ctx context.Context, accessJSON, scopeJSON, id, changeJSON string) (*platform.Reply, error) {
	a, ok := applicationAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	var scope *application.Scope
	var change calendar.Change
	if !appDecode(changeJSON, &change) {
		return appReply(nil, application.ErrInvalid)
	}
	if transport.CallerRole(ctx) == "runtime" {
		if !appDecode(scopeJSON, &scope) || scope == nil || scope.Access != a {
			return appReply(nil, application.ErrDenied)
		}
	} else if scopeJSON != "null" {
		return appReply(nil, application.ErrDenied)
	}
	v, err := h.service.Change(ctx, a, scope, id, change)
	return appReply(v, err)
}
func (h *calendarHandler) Assignment(ctx context.Context, scopeJSON, id string, epoch int64) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	if !appDecode(scopeJSON, &scope) {
		return appReply(nil, application.ErrInvalid)
	}
	v, err := h.service.Assignment(ctx, scope, id, epoch)
	return appReply(v, err)
}
func (h *calendarHandler) CancelCommand(ctx context.Context, scopeJSON, id string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	if !appDecode(scopeJSON, &scope) {
		return appReply(nil, application.ErrInvalid)
	}
	return appReply(nil, h.service.CancelCommand(ctx, scope, id))
}

func (h *calendarHandler) AssignTrigger(ctx context.Context, scopeJSON, id string, epoch int64) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	if !appDecode(scopeJSON, &scope) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.AssignTrigger(ctx, scope, id, epoch)
	return appReply(value, err)
}
