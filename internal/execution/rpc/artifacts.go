package rpc

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (c *Client) BeginArtifact(ctx context.Context, user, tenant, agent string, request execution.ArtifactRequest) (execution.ArtifactUpload, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return execution.ArtifactUpload{}, err
	}
	reply, err := c.client.BeginArtifact(ctx, actor(user, tenant, agent), string(encoded))
	var result execution.ArtifactUpload
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) WriteArtifact(ctx context.Context, user, tenant, agent, id string, chunk execprotocol.FileChunk) (execution.ArtifactUpload, error) {
	if chunk.Validate() != nil {
		return execution.ArtifactUpload{}, execprotocol.ErrInvalid
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return execution.ArtifactUpload{}, err
	}
	reply, err := c.client.WriteArtifact(ctx, actor(user, tenant, agent), id, string(encoded))
	var result execution.ArtifactUpload
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) CommitArtifact(ctx context.Context, user, tenant, agent, id string) (execution.Artifact, error) {
	reply, err := c.client.CommitArtifact(ctx, actor(user, tenant, agent), id)
	var result execution.Artifact
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) Artifact(ctx context.Context, user, tenant, agent, id string) (execution.Artifact, error) {
	reply, err := c.client.Artifact(ctx, actor(user, tenant, agent), id)
	var result execution.Artifact
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) Artifacts(ctx context.Context, user, tenant, agent, after string, limit int) ([]execution.Artifact, error) {
	if limit < 1 || limit > 100 {
		return nil, execprotocol.ErrInvalid
	}
	reply, err := c.client.Artifacts(ctx, actor(user, tenant, agent), after, int32(limit))
	var result []execution.Artifact
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) ReadArtifact(ctx context.Context, user, tenant, agent, id string, offset int64, limit int) (execprotocol.FileChunk, error) {
	if offset < 0 || limit < 1 || limit > execprotocol.FileChunkBytes {
		return execprotocol.FileChunk{}, execprotocol.ErrInvalid
	}
	reply, err := c.client.ReadArtifact(ctx, actor(user, tenant, agent), id, offset, int32(limit))
	var result execprotocol.FileChunk
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) DeleteArtifact(ctx context.Context, user, tenant, agent, id string) error {
	reply, err := c.client.DeleteArtifact(ctx, actor(user, tenant, agent), id)
	return decode(reply, err, nil)
}
