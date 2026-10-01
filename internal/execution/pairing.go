package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func Digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

type Service struct {
	Admission maintenance.Admission
	Store     Repository
	Authority Authority
	Hosted    *HostedManager
	Blobs     *ArtifactManager
	Transfers TransferRepository
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
func validCapabilities(capabilities []execprotocol.Capability) bool {
	if len(capabilities) == 0 || len(capabilities) > 3 {
		return false
	}
	seen := map[execprotocol.Capability]bool{}
	for _, capability := range capabilities {
		if seen[capability] || (capability != execprotocol.Files && capability != execprotocol.Shell && capability != execprotocol.MCP) {
			return false
		}
		seen[capability] = true
	}
	return true
}

func (s *Service) BeginPair(ctx context.Context, request PairRequest) (Pairing, error) {
	done, err := maintenance.Enter(s.Admission)
	if err != nil {
		return Pairing{}, err
	}
	defer done()
	for _, character := range request.ID {
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_'
		if !valid {
			return Pairing{}, execprotocol.ErrInvalid
		}
	}
	if len(request.ID) < 20 || len(request.ID) > 100 || len(strings.TrimSpace(request.Name)) == 0 || len(request.Name) > 200 || (request.OS != "linux" && request.OS != "darwin") || !path.IsAbs(request.WorkingDirectory) || len(request.WorkingDirectory) > 4096 || !validDigest(request.PairSecretHash) || !validDigest(request.CredentialHash) || !validCapabilities(request.Capabilities) {
		return Pairing{}, execprotocol.ErrInvalid
	}
	pair, err := s.Store.BeginPair(ctx, request)
	// The registration payload contains digests, not proof of either secret.
	// Replaying it must not disclose the locally confirmed approval challenge.
	pair.Owner, pair.Grants, pair.AgentEpochs, pair.ApprovalNonce = OwnerScope{}, nil, nil, ""
	return pair, err
}

func (s *Service) PollPair(ctx context.Context, id, secret string) (Pairing, error) {
	if len(secret) < 32 || len(secret) > 256 {
		return Pairing{}, execprotocol.ErrDenied
	}
	return s.Store.Pair(ctx, id, Digest(secret))
}

func (s *Service) PreviewPair(ctx context.Context, actor, tenant, id string) (Pairing, error) {
	owner, err := s.Authority.Owner(ctx, actor, tenant, actor, true)
	if err != nil {
		return Pairing{}, err
	}
	pair, err := s.Store.Pair(ctx, id, "")
	if err != nil {
		return pair, err
	}
	if pair.State != "pending" && (pair.Owner.UserID != owner.UserID || pair.Owner.TenantID != owner.TenantID) {
		return Pairing{}, execprotocol.ErrDenied
	}
	// The browser does not receive the nonce used for the local confirmation.
	pair.ApprovalNonce = ""
	return pair, nil
}

func (s *Service) ApprovePair(ctx context.Context, actor, tenant, id string, grants map[string][]execprotocol.Capability) (Pairing, error) {
	owner, err := s.Authority.Owner(ctx, actor, tenant, actor, true)
	if err != nil {
		return Pairing{}, err
	}
	pair, err := s.Store.Pair(ctx, id, "")
	if err != nil {
		return pair, err
	}
	if pair.State != "pending" || len(grants) == 0 || len(grants) > 100 {
		return Pairing{}, execprotocol.ErrConflict
	}
	pair.Owner, pair.Grants, pair.AgentEpochs = owner, grants, map[string]int64{}
	for agent, capabilities := range grants {
		if !validCapabilities(capabilities) {
			return Pairing{}, execprotocol.ErrInvalid
		}
		for _, capability := range capabilities {
			if !slices.Contains(pair.Capabilities, capability) {
				return Pairing{}, execprotocol.ErrDenied
			}
		}
		scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
		if err != nil {
			return Pairing{}, err
		}
		if scope.UserID != actor || scope.FleetID != owner.FleetID {
			return Pairing{}, execprotocol.ErrDenied
		}
		pair.AgentEpochs[agent] = scope.AgentExecutionEpoch
	}
	pair.ApprovalNonce = rand.Text()
	pair, err = s.Store.ApprovePair(ctx, pair)
	pair.ApprovalNonce = ""
	return pair, err
}

func (s *Service) ConfirmPair(ctx context.Context, confirmation PairConfirmation) (Device, error) {
	done, err := maintenance.Enter(s.Admission)
	if err != nil {
		return Device{}, err
	}
	defer done()
	if len(confirmation.Credential) < 32 || len(confirmation.Credential) > 256 || len(confirmation.ApprovalNonce) < 20 || len(confirmation.ApprovalNonce) > 256 {
		return Device{}, execprotocol.ErrDenied
	}
	pair, err := s.PollPair(ctx, confirmation.ID, confirmation.Secret)
	if err != nil {
		return Device{}, err
	}
	if pair.State != "approved" && pair.State != "confirmed" {
		return Device{}, execprotocol.ErrConflict
	}
	owner, err := s.Authority.Owner(ctx, pair.Owner.UserID, pair.Owner.TenantID, pair.Owner.UserID, true)
	if err != nil {
		return Device{}, err
	}
	if owner.RemovalEpoch != pair.Owner.RemovalEpoch || owner.MembershipExecutionEpoch != pair.Owner.MembershipExecutionEpoch || owner.FleetID != pair.Owner.FleetID {
		return Device{}, execprotocol.ErrDenied
	}
	for agent, epoch := range pair.AgentEpochs {
		scope, err := s.Authority.Agent(ctx, owner.UserID, owner.TenantID, agent, true)
		if err != nil {
			return Device{}, err
		}
		if scope.AgentExecutionEpoch != epoch || scope.FleetID != owner.FleetID {
			return Device{}, execprotocol.ErrDenied
		}
	}
	return s.Store.ConfirmPair(ctx, confirmation)
}

func (s *Service) Devices(ctx context.Context, actor, tenant, owner string) ([]Device, error) {
	scope, err := s.Authority.Owner(ctx, actor, tenant, owner, false)
	if err != nil {
		return nil, err
	}
	devices, err := s.Store.Devices(ctx, scope)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(devices, func(device Device) bool { return device.Kind != "native" }), nil
}

func (s *Service) Revoke(ctx context.Context, actor, tenant, id string) error {
	device, err := s.Store.Device(ctx, id)
	if err != nil {
		return err
	}
	if device.TenantID != tenant || device.Kind != "native" {
		return execprotocol.ErrDenied
	}
	if _, err := s.Authority.Owner(ctx, actor, tenant, device.UserID, false); err != nil {
		return err
	}
	return s.Store.Revoke(ctx, id, actor)
}

func (s *Service) Restrict(ctx context.Context, actor, tenant, id string, version int64, grants map[string][]execprotocol.Capability) (Device, error) {
	device, err := s.Store.Device(ctx, id)
	if err != nil {
		return Device{}, err
	}
	if device.TenantID != tenant || device.UserID != actor || device.Kind != "native" {
		return Device{}, execprotocol.ErrDenied
	}
	owner, err := s.Authority.Owner(ctx, actor, tenant, actor, true)
	if err != nil {
		return Device{}, err
	}
	if owner.RemovalEpoch != device.RemovalEpoch {
		return Device{}, execprotocol.ErrDenied
	}
	if grants == nil {
		grants = map[string][]execprotocol.Capability{}
	}
	for agent, capabilities := range grants {
		if !validCapabilities(capabilities) {
			return Device{}, execprotocol.ErrInvalid
		}
		for _, capability := range capabilities {
			if !slices.Contains(device.Ceiling[agent], capability) {
				return Device{}, execprotocol.ErrDenied
			}
		}
	}
	return s.Store.SetGrants(ctx, id, version, grants, actor)
}
