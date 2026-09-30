package execution

import (
	"context"
	"mime"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// Artifact is an immutable object. Fleet publication is explicit; staging
// bytes and private attachments remain within the originating Agent.
type Artifact struct {
	ID        string          `json:"id"`
	Scope     Scope           `json:"scope"`
	Request   ArtifactRequest `json:"request"`
	State     string          `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
}

type ArtifactSource struct {
	EnvironmentID        string `json:"environment_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
	OperationID          string `json:"operation_id"`
	Path                 string `json:"path"`
	WorkingDirectory     string `json:"working_directory,omitempty"`
}

type ArtifactRequest struct {
	RequestID  string                    `json:"request_id"`
	Name       string                    `json:"name"`
	MediaType  string                    `json:"media_type"`
	Visibility string                    `json:"visibility"`
	Manifest   execprotocol.FileManifest `json:"manifest"`
	Source     *ArtifactSource           `json:"source,omitempty"`
}

func (r ArtifactRequest) Validate() error {
	if len(r.RequestID) < 1 || len(r.RequestID) > 200 || strings.TrimSpace(r.RequestID) != r.RequestID || len(r.Name) < 1 || len(r.Name) > 255 || strings.ContainsAny(r.Name, "/\\") || strings.IndexFunc(r.Name, unicode.IsControl) >= 0 || r.Name == "." || r.Name == ".." || r.Manifest.Validate() != nil || r.Visibility != "agent" && r.Visibility != "fleet" {
		return execprotocol.ErrInvalid
	}
	if len(r.MediaType) > 255 || strings.IndexFunc(r.MediaType, unicode.IsControl) >= 0 {
		return execprotocol.ErrInvalid
	}
	if _, _, err := mime.ParseMediaType(r.MediaType); err != nil {
		return execprotocol.ErrInvalid
	}
	if r.Source != nil {
		if _, err := uuid.Parse(r.Source.EnvironmentID); err != nil || r.Source.AuthorizationVersion < 1 || r.Source.OperationID == "" || len(r.Source.OperationID) > 200 || r.Source.Path == "" || len(r.Source.Path) > 4096 || strings.IndexByte(r.Source.Path, 0) >= 0 || len(r.Source.WorkingDirectory) > 4096 || strings.IndexByte(r.Source.WorkingDirectory, 0) >= 0 {
			return execprotocol.ErrInvalid
		}
	}
	return nil
}

func (a Artifact) OwnedBy(scope Scope) bool {
	return a.Scope.TenantID == scope.TenantID && a.Scope.UserID == scope.UserID && a.Scope.FleetID == scope.FleetID
}

func (a Artifact) ReadableBy(scope Scope) bool {
	return a.OwnedBy(scope) && a.State != "purging" && a.State != "deleted" && (a.Scope.AgentID == scope.AgentID || a.State == "ready" && a.Request.Visibility == "fleet")
}

type ArtifactRepository interface {
	ReserveArtifact(context.Context, Scope, ArtifactRequest, int64) (Artifact, error)
	Artifact(context.Context, Scope, string) (Artifact, error)
	Artifacts(context.Context, Scope, string, int) ([]Artifact, error)
	PublishArtifact(context.Context, Scope, string) (Artifact, error)
	BeginArtifactPurge(context.Context, Scope, string) (Artifact, error)
	FinishArtifactPurge(context.Context, string) error
	ArtifactPurges(context.Context, int) ([]string, error)
}
