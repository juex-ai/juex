package execution

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type ArtifactUpload struct {
	Artifact Artifact `json:"artifact"`
	Cursor   int64    `json:"cursor"`
}

type ArtifactManager struct {
	// The local volume has exactly one process owner. This lock keeps database
	// transitions and byte cleanup ordered, including retries after a crash.
	mu       sync.Mutex
	Store    ArtifactRepository
	Objects  *blob.Store
	Capacity int64
}

func (m *ArtifactManager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, err := m.Store.ArtifactPurges(ctx, 100)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.Objects.Remove(id); err != nil {
			return err
		}
		if err := m.Store.FinishArtifactPurge(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) artifactManager() (*ArtifactManager, error) {
	if s.Blobs == nil || s.Blobs.Store == nil || s.Blobs.Objects == nil || s.Blobs.Capacity < 1 {
		return nil, execprotocol.ErrUnavailable
	}
	return s.Blobs, nil
}

func (s *Service) BeginArtifact(ctx context.Context, actor, tenant, agent string, request ArtifactRequest) (ArtifactUpload, error) {
	manager, err := s.artifactManager()
	if err != nil {
		return ArtifactUpload{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return ArtifactUpload{}, err
	}
	if err := request.Validate(); err != nil {
		return ArtifactUpload{}, err
	}
	if request.Source != nil {
		device, err := s.Store.Device(ctx, request.Source.EnvironmentID)
		if err != nil {
			return ArtifactUpload{}, err
		}
		if !permits(device, scope, "read") {
			return ArtifactUpload{}, execprotocol.ErrDenied
		}
		operation, err := s.Store.Operation(ctx, device.ID, request.Source.OperationID, 0, 4096)
		if err != nil {
			return ArtifactUpload{}, err
		}
		var manifest execprotocol.FileManifest
		var arguments struct {
			Path string `json:"path"`
		}
		if !operation.Scope.SameAuthority(scope) || operation.Request.Kind != "export_file" || operation.State != "completed" || json.Unmarshal(operation.Snapshot.Output, &manifest) != nil || manifest != request.Manifest || json.Unmarshal(operation.Request.Arguments, &arguments) != nil || arguments.Path != request.Source.Path {
			return ArtifactUpload{}, execprotocol.ErrDenied
		}
	}
	artifact, err := manager.Store.ReserveArtifact(ctx, scope, request, manager.Capacity)
	if err != nil {
		return ArtifactUpload{}, err
	}
	status, err := manager.Objects.Begin(artifact.ID, artifact.Request.Manifest)
	if err != nil {
		return ArtifactUpload{}, err
	}
	return ArtifactUpload{Artifact: artifact, Cursor: status.Cursor}, nil
}

func writableArtifact(ctx context.Context, manager *ArtifactManager, scope Scope, id string) (Artifact, error) {
	artifact, err := manager.Store.Artifact(ctx, scope, id)
	if err != nil {
		return artifact, err
	}
	if !scope.CanExecute || !artifact.Scope.SameAuthority(scope) {
		return Artifact{}, execprotocol.ErrDenied
	}
	return artifact, nil
}

func (s *Service) WriteArtifact(ctx context.Context, actor, tenant, agent, id string, chunk execprotocol.FileChunk) (ArtifactUpload, error) {
	manager, err := s.artifactManager()
	if err != nil {
		return ArtifactUpload{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return ArtifactUpload{}, err
	}
	artifact, err := writableArtifact(ctx, manager, scope, id)
	if err != nil {
		return ArtifactUpload{}, err
	}
	status, err := manager.Objects.Write(id, chunk)
	if err != nil {
		return ArtifactUpload{}, err
	}
	return ArtifactUpload{Artifact: artifact, Cursor: status.Cursor}, nil
}

func (s *Service) CommitArtifact(ctx context.Context, actor, tenant, agent, id string) (Artifact, error) {
	manager, err := s.artifactManager()
	if err != nil {
		return Artifact{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := writableArtifact(ctx, manager, scope, id); err != nil {
		return Artifact{}, err
	}
	if _, err := manager.Objects.Commit(id); err != nil {
		return Artifact{}, err
	}
	// Hashing a large object can outlive a suspension or permission change.
	// A durable local rename alone never authorizes publication.
	fresh, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return Artifact{}, err
	}
	if !fresh.SameAuthority(scope) {
		return Artifact{}, execprotocol.ErrDenied
	}
	return manager.Store.PublishArtifact(ctx, fresh, id)
}

func (s *Service) Artifact(ctx context.Context, actor, tenant, agent, id string) (Artifact, error) {
	manager, err := s.artifactManager()
	if err != nil {
		return Artifact{}, err
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return Artifact{}, err
	}
	return manager.Store.Artifact(ctx, scope, id)
}

func (s *Service) Artifacts(ctx context.Context, actor, tenant, agent, after string, limit int) ([]Artifact, error) {
	manager, err := s.artifactManager()
	if err != nil {
		return nil, err
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return nil, err
	}
	return manager.Store.Artifacts(ctx, scope, after, limit)
}

func (s *Service) ReadArtifact(ctx context.Context, actor, tenant, agent, id string, offset int64, limit int) (execprotocol.FileChunk, error) {
	manager, err := s.artifactManager()
	if err != nil {
		return execprotocol.FileChunk{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	artifact, err := s.Artifact(ctx, actor, tenant, agent, id)
	if err != nil {
		return execprotocol.FileChunk{}, err
	}
	if artifact.State != "ready" {
		return execprotocol.FileChunk{}, execprotocol.ErrConflict
	}
	return manager.Objects.Read(id, offset, limit)
}

func (s *Service) DeleteArtifact(ctx context.Context, actor, tenant, agent, id string) error {
	manager, err := s.artifactManager()
	if err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return err
	}
	if _, err := manager.Store.BeginArtifactPurge(ctx, scope, id); err != nil {
		return err
	}
	if err := manager.Objects.Remove(id); err != nil {
		return err
	}
	return manager.Store.FinishArtifactPurge(ctx, id)
}
