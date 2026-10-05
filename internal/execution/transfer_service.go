package execution

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func (s *Service) BeginTransfer(ctx context.Context, actor, tenant, agent string, request TransferRequest) (Transfer, error) {
	return s.beginTransfer(ctx, actor, tenant, agent, request, nil)
}

func (s *Service) BeginTransferFenced(ctx context.Context, actor, tenant, agent string, request TransferRequest, fence execprotocol.AuthorityFence) (Transfer, error) {
	if fence.ActorEpoch < 1 || fence.MembershipEpoch < 1 || fence.AgentEpoch < 1 {
		return Transfer{}, execprotocol.ErrDenied
	}
	return s.beginTransfer(ctx, actor, tenant, agent, request, &fence)
}

func (s *Service) beginTransfer(ctx context.Context, actor, tenant, agent string, request TransferRequest, fence *execprotocol.AuthorityFence) (Transfer, error) {
	done, err := maintenance.Enter(s.Admission)
	if err != nil {
		return Transfer{}, err
	}
	defer done()
	if s.Transfers == nil {
		return Transfer{}, execprotocol.ErrUnavailable
	}
	if _, err := s.artifactManager(); err != nil {
		return Transfer{}, err
	}
	if err := request.Validate(); err != nil {
		return Transfer{}, err
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return Transfer{}, err
	}
	if fence != nil && (scope.ActorAuthorizationEpoch != fence.ActorEpoch || scope.MembershipExecutionEpoch != fence.MembershipEpoch || scope.AgentExecutionEpoch != fence.AgentEpoch) {
		return Transfer{}, execprotocol.ErrDenied
	}
	transfer := Transfer{Scope: scope, Request: request}
	if err := s.transferAuthority(ctx, transfer); err != nil {
		return Transfer{}, err
	}
	var artifact *Artifact
	if request.ArtifactID != "" {
		value, err := s.Artifact(ctx, actor, tenant, agent, request.ArtifactID)
		if err != nil {
			return Transfer{}, err
		}
		if value.State != "ready" {
			return Transfer{}, execprotocol.ErrConflict
		}
		artifact = &value
	}
	admitted, err := s.Transfers.AdmitTransfer(ctx, scope, request, artifact)
	if err != nil {
		return Transfer{}, err
	}
	return s.transferProgress(ctx, admitted)
}

func (s *Service) Transfer(ctx context.Context, actor, tenant, agent, id string) (Transfer, error) {
	transfer, err := s.authorizedTransfer(ctx, actor, tenant, agent, id)
	if err != nil {
		return Transfer{}, err
	}
	return s.transferProgress(ctx, transfer)
}

func (s *Service) authorizedTransfer(ctx context.Context, actor, tenant, agent, id string) (Transfer, error) {
	if s.Transfers == nil {
		return Transfer{}, execprotocol.ErrUnavailable
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return Transfer{}, err
	}
	transfer, err := s.Transfers.Transfer(ctx, id)
	if err != nil {
		return Transfer{}, err
	}
	if transfer.Scope.AgentID != scope.AgentID || transfer.Scope.TenantID != scope.TenantID || transfer.Scope.UserID != scope.UserID || transfer.Scope.FleetID != scope.FleetID {
		return Transfer{}, execprotocol.ErrDenied
	}
	return transfer, nil
}

func (s *Service) transferProgress(ctx context.Context, transfer Transfer) (Transfer, error) {
	if transfer.State.Terminal() || transfer.CancelRequested {
		return transfer, nil
	}
	// Accepted covers both source capture and destination import. Only the
	// current durable operation tells us whether a device is still awaited.
	source := transfer.Request.Target == nil || transfer.ArtifactID == ""
	location := transfer.Request.Target
	if source {
		location = transfer.Request.Source
	}
	if location == nil {
		return Transfer{}, execprotocol.ErrInvalid
	}
	operation, err := s.Store.Operation(ctx, location.EnvironmentID, transfer.FileOperation(source, nil).ID, 0, 1)
	if err != nil {
		return Transfer{}, err
	}
	transfer.WaitReason = "execution"
	if operation.State == "waiting" && !operation.CancelRequested {
		transfer.WaitReason = "environment"
	}
	return transfer, nil
}

func (s *Service) CancelTransfer(ctx context.Context, actor, tenant, agent, id string) error {
	if _, err := s.authorizedTransfer(ctx, actor, tenant, agent, id); err != nil {
		return err
	}
	return s.Transfers.CancelTransfer(ctx, id)
}

func (s *Service) ListTransfers(ctx context.Context, actor, tenant, agent, after string, limit int) ([]Transfer, error) {
	if s.Transfers == nil {
		return nil, execprotocol.ErrUnavailable
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return nil, err
	}
	transfers, err := s.Transfers.ListTransfers(ctx, scope, after, limit)
	if err != nil {
		return nil, err
	}
	for i := range transfers {
		transfers[i], err = s.transferProgress(ctx, transfers[i])
		if err != nil {
			return nil, err
		}
	}
	return transfers, nil
}

func (s *Service) ExtendTransfer(ctx context.Context, actor, tenant, agent, id string, wait time.Duration) error {
	if wait < time.Minute || wait > 30*24*time.Hour {
		return execprotocol.ErrInvalid
	}
	transfer, err := s.authorizedTransfer(ctx, actor, tenant, agent, id)
	if err != nil {
		return err
	}
	if err := s.transferAuthority(ctx, transfer); err != nil {
		return err
	}
	return s.Transfers.ExtendTransfer(ctx, id, wait)
}

func (s *Service) transferAuthority(ctx context.Context, transfer Transfer) error {
	scope, err := s.Authority.Agent(ctx, transfer.Scope.ActorID, transfer.Scope.TenantID, transfer.Scope.AgentID, true)
	if err != nil {
		return err
	}
	if !scope.SameAuthority(transfer.Scope) || !scope.Capabilities.Allows(agentpolicy.Files) {
		return execprotocol.ErrDenied
	}
	for _, location := range []*FileLocation{transfer.Request.Source, transfer.Request.Target} {
		if location == nil {
			continue
		}
		device, err := s.Store.Device(ctx, location.EnvironmentID)
		if err != nil {
			return err
		}
		if !location.Permits(device, scope) {
			return execprotocol.ErrDenied
		}
	}
	return nil
}

func (s *Service) reconcileTransfers(ctx context.Context) error {
	if s.Transfers == nil {
		return nil
	}
	transfers, err := s.Transfers.ActiveTransfers(ctx, 128)
	if err != nil {
		return err
	}
	for _, transfer := range transfers {
		if err := s.reconcileTransfer(ctx, transfer); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileTransfer(ctx context.Context, transfer Transfer) error {
	if !transfer.CancelRequested {
		err := s.transferAuthority(ctx, transfer)
		if err != nil && !errors.Is(err, execprotocol.ErrDenied) {
			return nil
		}
		if err != nil || !time.Now().Before(transfer.WaitUntil) {
			if err := s.Transfers.CancelTransfer(ctx, transfer.ID); err != nil {
				return err
			}
			transfer.CancelRequested = true
		}
	}
	// An already published destination remains an observed success even when
	// cancellation raced with it. Cancellation never implies an external undo.
	source := transfer.Request.Target == nil || transfer.ArtifactID == ""
	location := transfer.Request.Target
	if source {
		location = transfer.Request.Source
	}
	if location == nil {
		return execprotocol.ErrInvalid
	}
	operation, err := s.Store.Operation(ctx, location.EnvironmentID, transfer.FileOperation(source, nil).ID, 0, 1024)
	if err != nil {
		return err
	}
	state := execprotocol.State(operation.State)
	if !state.Terminal() {
		return nil
	}
	if state != execprotocol.Completed {
		return s.Transfers.FinishTransfer(ctx, transfer.ID, state, operation.Snapshot.Error)
	}
	if !source || transfer.ArtifactID != "" {
		return s.Transfers.FinishTransfer(ctx, transfer.ID, execprotocol.Completed, "")
	}
	if transfer.CancelRequested {
		return s.Transfers.FinishTransfer(ctx, transfer.ID, execprotocol.Cancelled, "transfer cancelled before publication")
	}
	return nil
}
