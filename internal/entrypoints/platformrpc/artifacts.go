package platformrpc

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
)

func (h *executionHandler) BeginArtifact(ctx context.Context, actor *platform.Actor, encoded string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	var request execution.ArtifactRequest
	if !validActor(actor) || len(encoded) > 16<<10 || json.Unmarshal([]byte(encoded), &request) != nil {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.BeginArtifact(ctx, actor.UserID, actor.TenantID, actor.AgentID, request)
	return executionReply(v, err)
}

func (h *executionHandler) WriteArtifact(ctx context.Context, actor *platform.Actor, id, encoded string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	var chunk execprotocol.FileChunk
	if !validActor(actor) || len(encoded) > 512<<10 || json.Unmarshal([]byte(encoded), &chunk) != nil {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.WriteArtifact(ctx, actor.UserID, actor.TenantID, actor.AgentID, id, chunk)
	return executionReply(v, err)
}

func (h *executionHandler) CommitArtifact(ctx context.Context, actor *platform.Actor, id string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.CommitArtifact(ctx, actor.UserID, actor.TenantID, actor.AgentID, id)
	return executionReply(v, err)
}

func (h *executionHandler) Artifact(ctx context.Context, actor *platform.Actor, id string) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.Artifact(ctx, actor.UserID, actor.TenantID, actor.AgentID, id)
	return executionReply(v, err)
}

func (h *executionHandler) Artifacts(ctx context.Context, actor *platform.Actor, after string, limit int32) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.Artifacts(ctx, actor.UserID, actor.TenantID, actor.AgentID, after, int(limit))
	return executionReply(v, err)
}

func (h *executionHandler) ReadArtifact(ctx context.Context, actor *platform.Actor, id string, offset int64, limit int32) (*platform.Reply, error) {
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	v, err := h.service.ReadArtifact(ctx, actor.UserID, actor.TenantID, actor.AgentID, id, offset, int(limit))
	return executionReply(v, err)
}

func (h *executionHandler) DeleteArtifact(ctx context.Context, actor *platform.Actor, id string) (*platform.Reply, error) {
	if !managementCaller(ctx) {
		return executionReply(nil, execprotocol.ErrDenied)
	}
	if !validActor(actor) {
		return executionReply(nil, execprotocol.ErrInvalid)
	}
	return executionReply(nil, h.service.DeleteArtifact(ctx, actor.UserID, actor.TenantID, actor.AgentID, id))
}
