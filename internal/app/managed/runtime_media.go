package managed

import (
	"context"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (g RuntimeTools) ReadMedia(ctx context.Context, scope managedruntime.Scope, ref llm.MediaRef) ([]byte, error) {
	if ref.ArtifactID == "" || ref.ArtifactPath != "" || g.Client == nil {
		return nil, execprotocol.ErrInvalid
	}
	artifact, err := g.Client.Artifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, ref.ArtifactID)
	if err != nil {
		return nil, err
	}
	manifest := artifact.Request.Manifest
	if artifact.State != "ready" || manifest.Validate() != nil || manifest.Size <= 0 || manifest.Size > llm.MaxProviderImageArtifactBytes || manifest.SHA256 != ref.SHA256 ||
		artifact.Scope.TenantID != scope.TenantID || artifact.Scope.UserID != scope.UserID || artifact.Scope.FleetID != scope.FleetID ||
		(ref.MediaType != "" && !strings.EqualFold(ref.MediaType, artifact.Request.MediaType)) {
		return nil, execprotocol.ErrInvalid
	}
	data := make([]byte, 0, int(manifest.Size))
	for int64(len(data)) < manifest.Size {
		limit := min(execprotocol.FileChunkBytes, int(manifest.Size)-len(data))
		chunk, err := g.Client.ReadArtifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, ref.ArtifactID, int64(len(data)), limit)
		if err != nil {
			return nil, err
		}
		if chunk.Validate() != nil || chunk.Offset != int64(len(data)) || len(chunk.Data) == 0 || len(chunk.Data) > limit {
			return nil, execprotocol.ErrInvalid
		}
		data = append(data, chunk.Data...)
	}
	if execprotocol.FileDigest(data) != manifest.SHA256 {
		return nil, execprotocol.ErrInvalid
	}
	return data, nil
}
