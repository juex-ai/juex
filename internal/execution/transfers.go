package execution

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type FileLocation struct {
	EnvironmentID        string `json:"environment_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
	Path                 string `json:"path"`
	WorkingDirectory     string `json:"working_directory,omitempty"`
}

func (l FileLocation) Permits(device Device, scope Scope) bool {
	return l.EnvironmentID == device.ID && l.AuthorizationVersion == device.Version && permits(device, scope, "read")
}

type TransferRequest struct {
	RequestID  string        `json:"request_id"`
	Source     *FileLocation `json:"source,omitempty"`
	Target     *FileLocation `json:"target,omitempty"`
	ArtifactID string        `json:"artifact_id,omitempty"`
	Name       string        `json:"name,omitempty"`
	MediaType  string        `json:"media_type,omitempty"`
	Visibility string        `json:"visibility,omitempty"`
}

func (r TransferRequest) Validate() error {
	if len(r.RequestID) < 1 || len(r.RequestID) > 200 || strings.TrimSpace(r.RequestID) != r.RequestID || (r.Source == nil) == (r.ArtifactID == "") || r.Source == nil && r.Target == nil {
		return execprotocol.ErrInvalid
	}
	for _, location := range []*FileLocation{r.Source, r.Target} {
		if location == nil {
			continue
		}
		if _, err := uuid.Parse(location.EnvironmentID); err != nil || location.AuthorizationVersion < 1 || location.Path == "" || len(location.Path) > 4096 || strings.IndexByte(location.Path, 0) >= 0 || len(location.WorkingDirectory) > 4096 || strings.IndexByte(location.WorkingDirectory, 0) >= 0 || location.WorkingDirectory != "" && !strings.HasPrefix(location.WorkingDirectory, "/") {
			return execprotocol.ErrInvalid
		}
	}
	if r.Source != nil {
		return (ArtifactRequest{RequestID: r.RequestID, Name: r.Name, MediaType: r.MediaType, Visibility: r.Visibility, Manifest: execprotocol.FileManifest{SHA256: execprotocol.FileDigest(nil)}}).Validate()
	}
	if _, err := uuid.Parse(r.ArtifactID); err != nil || r.Name != "" || r.MediaType != "" || r.Visibility != "" {
		return execprotocol.ErrInvalid
	}
	return nil
}

type Transfer struct {
	ID              string             `json:"id"`
	Scope           Scope              `json:"scope"`
	Request         TransferRequest    `json:"request"`
	State           execprotocol.State `json:"state"`
	WaitReason      string             `json:"wait_reason,omitempty"`
	ArtifactID      string             `json:"artifact_id"`
	CancelRequested bool               `json:"cancel_requested"`
	Error           string             `json:"error"`
	WaitUntil       time.Time          `json:"wait_until"`
	CreatedAt       time.Time          `json:"created_at"`
}

// TransferID lets a durable caller recover a lost admission response without
// issuing a new transfer or retaining a process-local handle.
func TransferID(agent, request string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("juex.transfer\x00"+agent+"\x00"+request)).String()
}

func (t Transfer) FileOperation(source bool, manifest *execprotocol.FileManifest) execprotocol.Request {
	location, kind, suffix := t.Request.Target, "import_file", "target"
	if source {
		location, kind, suffix = t.Request.Source, "export_file", "source"
	}
	arguments, _ := json.Marshal(execprotocol.FileTransferArguments{Path: location.Path, WorkingDirectory: location.WorkingDirectory, Manifest: manifest})
	return execprotocol.Request{Version: execprotocol.Version, ID: "transfer/" + t.ID + "/" + suffix, AgentID: t.Scope.AgentID, Kind: kind, Arguments: arguments}
}

type TransferRepository interface {
	AdmitTransfer(context.Context, Scope, TransferRequest, *Artifact) (Transfer, error)
	Transfer(context.Context, string) (Transfer, error)
	ListTransfers(context.Context, Scope, string, int) ([]Transfer, error)
	ActiveTransfers(context.Context, int) ([]Transfer, error)
	AttachTransferArtifact(context.Context, Transfer, Artifact) (Transfer, error)
	CancelTransfer(context.Context, string) error
	CancelPreparedTransfer(context.Context, Scope, string) (execprotocol.State, error)
	FinishTransfer(context.Context, string, execprotocol.State, string) error
	SetTransferNotice(context.Context, string, string) error
	ExtendTransfer(context.Context, string, time.Duration) error
}
