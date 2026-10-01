package execution

import (
	"context"
	"errors"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (s *Service) operationTransfer(ctx context.Context, operation Operation) (Transfer, error) {
	if s.Transfers == nil {
		return Transfer{}, execprotocol.ErrUnavailable
	}
	parts := strings.Split(operation.ID, "/")
	if len(parts) != 3 || parts[0] != "transfer" {
		return Transfer{}, execprotocol.ErrInvalid
	}
	transfer, err := s.Transfers.Transfer(ctx, parts[1])
	if err != nil {
		return transfer, err
	}
	source := operation.Request.Kind == "export_file"
	location := transfer.Request.Target
	if source {
		location = transfer.Request.Source
	}
	if location == nil || location.EnvironmentID != operation.EnvironmentID || operation.ID != transfer.FileOperation(source, nil).ID || !transfer.Scope.SameAuthority(operation.Scope) {
		return Transfer{}, execprotocol.ErrDenied
	}
	return transfer, nil
}

// File chunks are exchanged only by the connection owner. Every page rechecks
// current authority and the original device grant version, including an ABA
// revoke/regrant that leaves the visible capability list unchanged.
func (s *Service) fileTransferCurrent(ctx context.Context, device Device, prior Transfer) (Transfer, error) {
	transfer, err := s.Transfers.Transfer(ctx, prior.ID)
	if err != nil {
		return transfer, err
	}
	if transfer.CancelRequested || transfer.State.Terminal() {
		return transfer, execprotocol.ErrDenied
	}
	if err := s.transferAuthority(ctx, transfer); err != nil {
		return transfer, err
	}
	fresh, err := s.Store.Device(ctx, device.ID)
	if err != nil {
		return transfer, err
	}
	if fresh.ConnectionEpoch != device.ConnectionEpoch {
		return transfer, execprotocol.ErrConflict
	}
	return transfer, nil
}

func (s *Service) reconcileFileOperation(ctx context.Context, device Device, operation Operation, exchange Exchange) (bool, error) {
	transfer, err := s.operationTransfer(ctx, operation)
	if err != nil {
		return false, err
	}
	if operation.Request.Kind == "export_file" && operation.State == "completed" {
		if operation.Snapshot.File == nil || !operation.Snapshot.File.Ready || operation.Snapshot.FileExpired {
			return false, execprotocol.ErrInvalid
		}
		manifest := operation.Snapshot.File.Manifest
		if transfer.CancelRequested || transfer.State.Terminal() && transfer.State != execprotocol.Completed {
			_, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "file_discard", AgentID: operation.Scope.AgentID, OperationID: operation.ID, FileManifest: &manifest})
			return err == nil, err
		}
		if transfer.ArtifactID != "" {
			_, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "file_ack", AgentID: operation.Scope.AgentID, OperationID: operation.ID, FileManifest: &manifest})
			return err == nil, err
		}
		return s.receiveTransferFile(ctx, device, operation, transfer, exchange)
	}
	if operation.Request.Kind == "import_file" && operation.State == "accepted" && !operation.CancelRequested {
		return false, s.sendTransferFile(ctx, device, operation, transfer, exchange)
	}
	return execprotocol.State(operation.State).Terminal(), nil
}

func (s *Service) receiveTransferFile(ctx context.Context, device Device, operation Operation, transfer Transfer, exchange Exchange) (bool, error) {
	current, err := s.fileTransferCurrent(ctx, device, transfer)
	if err != nil {
		return false, s.fileTransferError(ctx, transfer, err)
	}
	scope := current.Scope
	request := ArtifactRequest{RequestID: "transfer/" + transfer.ID, Name: transfer.Request.Name, MediaType: transfer.Request.MediaType, Visibility: transfer.Request.Visibility, Manifest: operation.Snapshot.File.Manifest, Source: &ArtifactSource{EnvironmentID: device.ID, AuthorizationVersion: transfer.Request.Source.AuthorizationVersion, OperationID: operation.ID, Path: transfer.Request.Source.Path, WorkingDirectory: transfer.Request.Source.WorkingDirectory}}
	upload, err := s.BeginArtifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, request)
	if err != nil {
		return false, s.fileTransferError(ctx, transfer, err)
	}
	if transfer.Error != "" {
		if err := s.Transfers.SetTransferNotice(ctx, transfer.ID, ""); err != nil {
			return false, err
		}
	}
	for pages := 0; upload.Cursor < request.Manifest.Size && pages < 4; pages++ {
		if _, err := s.fileTransferCurrent(ctx, device, transfer); err != nil {
			return false, s.fileTransferError(ctx, transfer, err)
		}
		reply, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "file_read", AgentID: scope.AgentID, OperationID: operation.ID, Cursor: upload.Cursor, Limit: execprotocol.FileChunkBytes})
		if err != nil {
			return false, err
		}
		if reply.FileChunk == nil || reply.FileChunk.Offset != upload.Cursor || len(reply.FileChunk.Data) == 0 || reply.FileChunk.Validate() != nil {
			return false, execprotocol.ErrInvalid
		}
		upload, err = s.WriteArtifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, upload.Artifact.ID, *reply.FileChunk)
		if err != nil {
			return false, s.fileTransferError(ctx, transfer, err)
		}
	}
	if upload.Cursor < request.Manifest.Size {
		return false, nil
	}
	if _, err := s.fileTransferCurrent(ctx, device, transfer); err != nil {
		return false, s.fileTransferError(ctx, transfer, err)
	}
	artifact, err := s.CommitArtifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, upload.Artifact.ID)
	if err != nil {
		return false, s.fileTransferError(ctx, transfer, err)
	}
	if _, err := s.fileTransferCurrent(ctx, device, transfer); err != nil {
		return false, s.fileTransferError(ctx, transfer, err)
	}
	if _, err := s.Transfers.AttachTransferArtifact(ctx, transfer, artifact); err != nil {
		return false, s.fileTransferError(ctx, transfer, err)
	}
	_, err = callDevice(ctx, exchange, execprotocol.Envelope{Type: "file_ack", AgentID: scope.AgentID, OperationID: operation.ID, FileManifest: &request.Manifest})
	return err == nil, err
}

func (s *Service) sendTransferFile(ctx context.Context, device Device, operation Operation, transfer Transfer, exchange Exchange) error {
	if _, err := s.fileTransferCurrent(ctx, device, transfer); err != nil {
		return s.fileTransferError(ctx, transfer, err)
	}
	scope := transfer.Scope
	artifact, err := s.Artifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, transfer.ArtifactID)
	if err != nil {
		return s.fileTransferError(ctx, transfer, err)
	}
	status := operation.Snapshot.File
	if artifact.State != "ready" || status == nil || status.Manifest != artifact.Request.Manifest || status.Cursor < 0 || status.Cursor > status.Manifest.Size {
		return execprotocol.ErrInvalid
	}
	cursor := status.Cursor
	for pages := 0; cursor < status.Manifest.Size && pages < 4; pages++ {
		if _, err := s.fileTransferCurrent(ctx, device, transfer); err != nil {
			return s.fileTransferError(ctx, transfer, err)
		}
		chunk, err := s.ReadArtifact(ctx, scope.ActorID, scope.TenantID, scope.AgentID, artifact.ID, cursor, execprotocol.FileChunkBytes)
		if err != nil {
			return s.fileTransferError(ctx, transfer, err)
		}
		reply, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "file_write", AgentID: scope.AgentID, OperationID: operation.ID, FileChunk: &chunk})
		if err != nil {
			return err
		}
		if reply.FileStatus == nil || reply.FileStatus.Manifest != status.Manifest || reply.FileStatus.Cursor < cursor+int64(len(chunk.Data)) || reply.FileStatus.Cursor > status.Manifest.Size {
			return execprotocol.ErrInvalid
		}
		cursor = reply.FileStatus.Cursor
	}
	if cursor < status.Manifest.Size {
		return nil
	}
	if _, err := s.fileTransferCurrent(ctx, device, transfer); err != nil {
		return s.fileTransferError(ctx, transfer, err)
	}
	reply, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "file_commit", AgentID: scope.AgentID, OperationID: operation.ID})
	if err != nil {
		return err
	}
	if reply.FileStatus == nil || !reply.FileStatus.Ready || reply.FileStatus.Manifest != status.Manifest {
		return execprotocol.ErrInvalid
	}
	return nil
}

func (s *Service) fileTransferError(ctx context.Context, transfer Transfer, err error) error {
	if errors.Is(err, execprotocol.ErrQuota) {
		return s.Transfers.SetTransferNotice(ctx, transfer.ID, "waiting_for_file_storage")
	}
	if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) {
		return s.Transfers.CancelTransfer(ctx, transfer.ID)
	}
	return err
}
