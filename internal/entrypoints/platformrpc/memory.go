package platformrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/cloudwego/kitex/server"
	"github.com/juex-ai/juex/internal/foundation/application"
	appwire "github.com/juex-ai/juex/internal/foundation/application/rpc"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	transport "github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	wire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/memory"
	"github.com/juex-ai/juex/internal/memory"
	"io"
	"net"
)

func NewMemory(listener net.Listener, credentials transport.Credentials, service *memory.Service, health func(context.Context) error) (server.Server, error) {
	options, err := transport.ServerOptions(listener, credentials, "management", "runtime")
	if err != nil {
		return nil, err
	}
	return newLifecycleServer(func(options ...server.Option) server.Server {
		return wire.NewServer(&memoryHandler{service: service, health: health}, options...)
	}, options), nil
}

type memoryHandler struct {
	service *memory.Service
	health  func(context.Context) error
}

func (h *memoryHandler) Recall(ctx context.Context, accessJSON, query string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok || transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	v, err := h.service.Recall(ctx, access, query)
	return appReply(v, err)
}

func (h *memoryHandler) Maintain(ctx context.Context, scopeJSON, thread, reason, commandID string) (*platform.Reply, error) {
	var scope application.Scope
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	if !appDecode(scopeJSON, &scope) {
		return appReply(nil, application.ErrInvalid)
	}
	v, err := h.service.Maintain(ctx, scope, thread, reason, commandID)
	return appReply(v, err)
}

func (h *memoryHandler) Contribute(ctx context.Context, scopeJSON, contributionJSON string) (*platform.Reply, error) {
	var scope application.Scope
	var batch memory.Contribution
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	if !appDecode(scopeJSON, &scope) || !appDecode(contributionJSON, &batch) {
		return appReply(nil, application.ErrInvalid)
	}
	return appReply(nil, h.service.Contribute(ctx, scope, batch))
}

func (h *memoryHandler) Reviews(ctx context.Context, accessJSON string, offset, limit int32) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok || transport.CallerRole(ctx) != "management" {
		return appReply(nil, application.ErrDenied)
	}
	value, err := h.service.Reviews(ctx, access, int(offset), int(limit))
	return appReply(value, err)
}
func (h *memoryHandler) StorageRules(ctx context.Context, accessJSON string, offset, limit int32) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok || transport.CallerRole(ctx) != "management" {
		return appReply(nil, application.ErrDenied)
	}
	value, err := h.service.StorageRules(ctx, access, int(offset), int(limit))
	return appReply(value, err)
}

func appReply(value any, err error) (*platform.Reply, error) {
	return transport.Reply(value, appwire.ErrorCode(err)), nil
}
func appDecode(text string, value any) bool {
	if len(text) > 512<<10 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && decoder.Decode(new(any)) == io.EOF
}
func memoryAccess(ctx context.Context, text string) (application.Access, bool) {
	var access application.Access
	if !appDecode(text, &access) {
		return access, false
	}
	role := transport.CallerRole(ctx)
	return access, (role == "management" && access.AgentID == "") || (role == "runtime" && access.AgentID != "")
}
func (h *memoryHandler) Health(ctx context.Context) (*platform.Reply, error) {
	return appReply(map[string]int{"protocol_version": 1}, h.health(ctx))
}
func (h *memoryHandler) Status(ctx context.Context, accessJSON string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	value, err := h.service.Status(ctx, access)
	return appReply(value, err)
}
func (h *memoryHandler) Search(ctx context.Context, accessJSON string, queryJSON string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	var query mc.Query
	if !appDecode(queryJSON, &query) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Search(ctx, access, query)
	return appReply(value, err)
}
func (h *memoryHandler) Read(ctx context.Context, accessJSON string, requestJSON string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	var request mc.ReadRequest
	if !appDecode(requestJSON, &request) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Read(ctx, access, request)
	return appReply(value, err)
}
func (h *memoryHandler) Facts(ctx context.Context, accessJSON string, queryJSON string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	var query mc.Query
	if !appDecode(queryJSON, &query) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Facts(ctx, access, query)
	return appReply(value, err)
}
func (h *memoryHandler) Domains(ctx context.Context, accessJSON string, requestJSON string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	var request mc.DomainRequest
	if !appDecode(requestJSON, &request) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Domains(ctx, access, request)
	return appReply(value, err)
}
func (h *memoryHandler) Administer(ctx context.Context, accessJSON string, requestJSON string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	if transport.CallerRole(ctx) != "management" {
		return appReply(nil, application.ErrDenied)
	}
	var request mc.AdminRequest
	if !appDecode(requestJSON, &request) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Administer(ctx, access, request)
	return appReply(value, err)
}
func (h *memoryHandler) Configure(ctx context.Context, accessJSON string, version int64, enabled bool, strategy string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok || transport.CallerRole(ctx) != "management" {
		return appReply(nil, application.ErrDenied)
	}
	value, err := h.service.Configure(ctx, access, version, enabled, strategy)
	return appReply(value, err)
}
func (h *memoryHandler) ReviewResult_(ctx context.Context, accessJSON, thread, id string) (*platform.Reply, error) {
	access, ok := memoryAccess(ctx, accessJSON)
	if !ok {
		return appReply(nil, application.ErrDenied)
	}
	value, err := h.service.Result(ctx, access, thread, id)
	return appReply(value, err)
}
func (h *memoryHandler) Propose(ctx context.Context, scopeJSON, thread, proposalJSON string, automatic bool, commandID string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	var proposal mc.Proposal
	if !appDecode(scopeJSON, &scope) || !appDecode(proposalJSON, &proposal) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Propose(ctx, scope, thread, proposal, automatic, commandID)
	return appReply(value, err)
}
func (h *memoryHandler) Review(ctx context.Context, scopeJSON, bindingJSON string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	var binding memory.Binding
	if !appDecode(scopeJSON, &scope) || !appDecode(bindingJSON, &binding) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Review(ctx, scope, binding)
	return appReply(value, err)
}
func (h *memoryHandler) Decide(ctx context.Context, scopeJSON, bindingJSON, decisionJSON string, commandID string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	var binding memory.Binding
	var decision mc.Decision
	if !appDecode(scopeJSON, &scope) || !appDecode(bindingJSON, &binding) || !appDecode(decisionJSON, &decision) {
		return appReply(nil, application.ErrInvalid)
	}
	value, err := h.service.Decide(ctx, scope, binding, decision, commandID)
	return appReply(value, err)
}

func (h *memoryHandler) CancelCommand(ctx context.Context, scopeJSON, id string) (*platform.Reply, error) {
	if transport.CallerRole(ctx) != "runtime" {
		return appReply(nil, application.ErrDenied)
	}
	var scope application.Scope
	if !appDecode(scopeJSON, &scope) {
		return appReply(nil, application.ErrInvalid)
	}
	return appReply(nil, h.service.CancelCommand(ctx, scope, id))
}
