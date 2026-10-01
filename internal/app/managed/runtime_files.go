package managed

import (
	"context"
	"errors"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func fileTransferResult(transfer execution.Transfer) managedruntime.FileTransferResult {
	return managedruntime.FileTransferResult{ID: transfer.ID, State: transfer.State, ArtifactID: transfer.ArtifactID, Error: transfer.Error, CancelRequested: transfer.CancelRequested, WaitUntil: transfer.WaitUntil}
}

func (g RuntimeTools) StartFileTransfer(ctx context.Context, scope managedruntime.Scope, requestID string, spec managedruntime.FileTransferSpec) (managedruntime.FileTransferResult, error) {
	result, err := g.FileTransfer(ctx, scope, execution.TransferID(scope.AgentID, requestID))
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, execprotocol.ErrNotFound) {
		if errors.Is(err, execprotocol.ErrDenied) {
			err = execprotocol.ErrOutcomeUnknown
		}
		return result, err
	}
	location := func(source *managedruntime.FileLocation) *execution.FileLocation {
		if source == nil {
			return nil
		}
		return &execution.FileLocation{EnvironmentID: source.EnvironmentID, AuthorizationVersion: source.AuthorizationVersion, Path: source.Path, WorkingDirectory: source.WorkingDirectory}
	}
	request := execution.TransferRequest{RequestID: requestID, Source: location(spec.Source), Target: location(spec.Target), ArtifactID: spec.ArtifactID, Name: spec.Name, MediaType: spec.MediaType, Visibility: spec.Visibility}
	transfer, err := g.Client.BeginTransferFenced(ctx, scope.ActorID, scope.TenantID, scope.AgentID, request, scope.ExecutionFence())
	return fileTransferResult(transfer), err
}

func (g RuntimeTools) FileTransfer(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.FileTransferResult, error) {
	transfer, err := g.Client.Transfer(ctx, scope.ActorID, scope.TenantID, scope.AgentID, id)
	return fileTransferResult(transfer), err
}

func (g RuntimeTools) CancelFileTransfer(ctx context.Context, scope managedruntime.Scope, id string) error {
	return g.Client.CancelTransfer(ctx, scope.ActorID, scope.TenantID, scope.AgentID, id)
}

func (g RuntimeTools) CancelFileRequest(ctx context.Context, scope managedruntime.Scope, requestID string) (execprotocol.State, error) {
	return g.Client.CancelPreparedTransfer(ctx, scope.ActorID, scope.TenantID, scope.AgentID, requestID)
}

func (g RuntimeTools) FileArtifacts(ctx context.Context, scope managedruntime.Scope, after string, limit int) ([]managedruntime.FileArtifact, error) {
	artifacts, err := g.Client.Artifacts(ctx, scope.ActorID, scope.TenantID, scope.AgentID, after, limit)
	if err != nil {
		return nil, err
	}
	result := make([]managedruntime.FileArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		item := managedruntime.FileArtifact{ID: artifact.ID, Name: artifact.Request.Name, MediaType: artifact.Request.MediaType, Visibility: artifact.Request.Visibility, State: artifact.State, Manifest: artifact.Request.Manifest}
		if artifact.Request.Source != nil {
			item.SourceEnvironmentID = artifact.Request.Source.EnvironmentID
		}
		result = append(result, item)
	}
	return result, nil
}
