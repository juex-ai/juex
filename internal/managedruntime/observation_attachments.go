package managedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// Stable source offsets identify captures across observer restarts. Pending
// captures keep the raw cursor unacknowledged; an unknown capture is reported
// with its original transfer handle and is never submitted under a new ID.
func (r toolRunner) captureObservationAttachments(ctx context.Context, source ObservationSource, batch *ObservationBatch) (bool, error) {
	for i := range batch.Facts {
		fact := &batch.Facts[i]
		if fact.Kind != "command.observation" {
			continue
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(fact.Data, &value); err != nil {
			return false, err
		}
		var attachments []ObservationAttachment
		if err := json.Unmarshal(value["attachments"], &attachments); err != nil {
			return false, err
		}
		for j := range attachments {
			attachment := &attachments[j]
			if r.files == nil {
				attachment.Error = "file service unavailable"
				continue
			}
			environments, err := r.gateway.Environments(ctx, source.Scope)
			if err != nil {
				return false, err
			}
			if !observationGrant(environments, source.EnvironmentID, source.AuthorizationVersion, execprotocol.Files) {
				attachment.Error = "file access is no longer authorized"
				continue
			}
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("juex.observation.attachment/%s/%s/%d/%d", source.EnvironmentID, source.OperationID, attachment.Offset, attachment.Ordinal))).String()
			name, media := attachment.Name, attachment.MediaType
			if name == "" {
				name = path.Base(attachment.Path)
			}
			if media == "" {
				media = "application/octet-stream"
			}
			transfer, err := r.files.StartFileTransfer(ctx, source.Scope, id, FileTransferSpec{Source: &FileLocation{EnvironmentID: source.EnvironmentID, AuthorizationVersion: source.AuthorizationVersion, Path: attachment.Path, WorkingDirectory: source.WorkingDirectory}, Name: name, MediaType: media, Visibility: "agent"})
			if err != nil {
				if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrInvalid) || errors.Is(err, execprotocol.ErrNotFound) || errors.Is(err, execprotocol.ErrQuota) {
					attachment.Error = err.Error()
					continue
				}
				return false, err
			}
			attachment.TransferID = transfer.ID
			switch transfer.State {
			case execprotocol.Completed:
				attachment.ArtifactID = transfer.ArtifactID
				attachment.Path = ""
			case execprotocol.Unknown:
				attachment.Error = "capture outcome unknown; inspect the original transfer"
			default:
				if !transfer.State.Terminal() {
					return false, nil
				}
				attachment.Error = transfer.Error
				if attachment.Error == "" {
					attachment.Error = string(transfer.State)
				}
			}
		}
		value["attachments"], _ = json.Marshal(attachments)
		// Unit metadata keeps ordering without exposing unsnapshotted file paths as
		// usable references alongside the authoritative Artifact list.
		var units []CommandObservationUnit
		if err := json.Unmarshal(value["units"], &units); err != nil {
			return false, err
		}
		for j := range units {
			units[j].Attachments = nil
		}
		value["units"], _ = json.Marshal(units)
		fact.Data, _ = json.Marshal(value)
	}
	return true, nil
}
