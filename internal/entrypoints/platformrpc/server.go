// Package platformrpc exposes private, mutually authenticated Kitex services.
package platformrpc

import (
	"context"
	"encoding/json"
	"net"

	"github.com/cloudwego/kitex/server"
	"github.com/juex-ai/juex/internal/foundation/llm"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	managementwire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/management"
	runtimewire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/runtime"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

type Authority interface {
	Peers(context.Context, managedruntime.Scope) ([]managedruntime.PeerAgent, error)
	AuthorizeFleet(context.Context, string, string, string, bool) (management.FleetAuthority, error)
	Authorize(context.Context, string, string, string, bool) (managedruntime.Scope, error)
	Snapshot(context.Context, managedruntime.Scope) (managedruntime.TurnConfig, error)
	Profile(context.Context, managedruntime.Scope, managedruntime.ModelConfig) (llm.ProviderProfile, error)
}

func NewManagement(listener net.Listener, credentials transport.Credentials, authority Authority) (server.Server, error) {
	opts, err := transport.ServerOptions(listener, credentials, "runtime", "execution", "memory", "calendar")
	if err != nil {
		return nil, err
	}
	return newLifecycleServer(func(options ...server.Option) server.Server {
		return managementwire.NewServer(&managementHandler{authority: authority}, options...)
	}, opts), nil
}

type managementHandler struct{ authority Authority }

func (h *managementHandler) AuthorizeFleet(ctx context.Context, actorID, tenantID, ownerID string, execute bool) (*platform.Reply, error) {
	if actorID == "" || tenantID == "" || ownerID == "" {
		return invalid()
	}
	value, err := h.authority.AuthorizeFleet(ctx, actorID, tenantID, ownerID, execute)
	return reply(value, err)
}

func reply(value any, err error) (*platform.Reply, error) {
	return transport.Reply(value, runtimeclient.ErrorCode(err)), nil
}
func invalid() (*platform.Reply, error) { return reply(nil, managedruntime.ErrInvalid) }
func validActor(a *platform.Actor) bool {
	return a != nil && a.UserID != "" && a.TenantID != "" && a.AgentID != ""
}
func (h *managementHandler) Authorize(ctx context.Context, actor *platform.Actor, execute bool) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	v, err := h.authority.Authorize(ctx, actor.UserID, actor.TenantID, actor.AgentID, execute)
	return reply(v, err)
}
func (h *managementHandler) Snapshot(ctx context.Context, scopeJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if len(scopeJSON) > 4096 || json.Unmarshal([]byte(scopeJSON), &scope) != nil {
		return invalid()
	}
	v, err := h.authority.Snapshot(ctx, scope)
	return reply(v, err)
}
func (h *managementHandler) ModelProfile(ctx context.Context, scopeJSON, configJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	var config managedruntime.ModelConfig
	if len(scopeJSON) > 4096 || len(configJSON) > 256<<10 || json.Unmarshal([]byte(scopeJSON), &scope) != nil || json.Unmarshal([]byte(configJSON), &config) != nil {
		return invalid()
	}
	v, err := h.authority.Profile(ctx, scope, config)
	return reply(v, err)
}

func NewRuntime(listener net.Listener, credentials transport.Credentials, service *managedruntime.Service, health func(context.Context) error) (server.Server, error) {
	opts, err := transport.ServerOptions(listener, credentials, "management")
	if err != nil {
		return nil, err
	}
	return newLifecycleServer(func(options ...server.Option) server.Server {
		return runtimewire.NewServer(&runtimeHandler{service: service, health: health}, options...)
	}, opts), nil
}

type runtimeHandler struct {
	service *managedruntime.Service
	health  func(context.Context) error
}

func (h *runtimeHandler) Health(ctx context.Context) (*platform.Reply, error) {
	return reply(map[string]int{"protocol_version": 1}, h.health(ctx))
}
func (h *runtimeHandler) Submit(ctx context.Context, actor *platform.Actor, requestID, threadID, text string) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	v, err := h.service.Submit(ctx, actor.UserID, actor.TenantID, actor.AgentID, managedruntime.InputRequest{RequestID: requestID, ThreadID: threadID, Text: text})
	return reply(v, err)
}
func (h *runtimeHandler) Threads(ctx context.Context, actor *platform.Actor) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	v, err := h.service.Threads(ctx, actor.UserID, actor.TenantID, actor.AgentID)
	return reply(v, err)
}
func (h *runtimeHandler) Timeline(ctx context.Context, actor *platform.Actor, threadID string, after int64, limit int32) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	v, err := h.service.Events(ctx, actor.UserID, actor.TenantID, actor.AgentID, threadID, after, int(limit))
	return reply(v, err)
}
func (h *runtimeHandler) Cancel(ctx context.Context, actor *platform.Actor, threadID string) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	err := h.service.Cancel(ctx, actor.UserID, actor.TenantID, actor.AgentID, threadID)
	return reply(nil, err)
}
func (h *runtimeHandler) CreateWorker(ctx context.Context, actor *platform.Actor, parentID, requestID, name string) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	v, err := h.service.Worker(ctx, actor.UserID, actor.TenantID, actor.AgentID, parentID, requestID, name)
	return reply(v, err)
}

func (h *runtimeHandler) Compact(ctx context.Context, actor *platform.Actor, thread, requestID, focus string) (*platform.Reply, error) {
	if !validActor(actor) {
		return invalid()
	}
	v, err := h.service.Compact(ctx, actor.UserID, actor.TenantID, actor.AgentID, thread, managedruntime.CompactionRequest{RequestID: requestID, Focus: focus})
	return reply(v, err)
}

func (h *managementHandler) Peers(ctx context.Context, scopeJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return reply(nil, managedruntime.ErrDenied)
	}
	var scope managedruntime.Scope
	if len(scopeJSON) > 4096 || json.Unmarshal([]byte(scopeJSON), &scope) != nil {
		return invalid()
	}
	result, err := h.authority.Peers(ctx, scope)
	return reply(result, err)
}

func (h *runtimeHandler) Archive(ctx context.Context, actor *platform.Actor, thread string, archived bool) (*platform.Reply, error) {
	if !validActor(actor) || thread == "" {
		return invalid()
	}
	result, err := h.service.Archive(ctx, actor.UserID, actor.TenantID, actor.AgentID, thread, archived)
	return reply(result, err)
}
