package execution

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func sameGeneration(a, b Scope) bool {
	return a.TenantID == b.TenantID && a.UserID == b.UserID && a.FleetID == b.FleetID && a.AgentID == b.AgentID && a.ActorID == b.ActorID && a.ActorAuthorizationEpoch == b.ActorAuthorizationEpoch && a.MembershipExecutionEpoch == b.MembershipExecutionEpoch && a.RemovalEpoch == b.RemovalEpoch && a.AgentExecutionEpoch == b.AgentExecutionEpoch
}

func permits(device Device, scope Scope, kind string) bool {
	return device.Status == "active" && scope.CanExecute && device.TenantID == scope.TenantID && device.UserID == scope.UserID && device.FleetID == scope.FleetID && device.RemovalEpoch == scope.RemovalEpoch && slices.Contains(device.Grants[scope.AgentID], execprotocol.RequiredCapability(kind))
}

func (s *Service) Environments(ctx context.Context, actor, tenant, agent string) ([]execprotocol.Environment, error) {
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return nil, err
	}
	if s.Hosted != nil {
		if err := s.Hosted.Ensure(ctx, scope); err != nil {
			return nil, err
		}
	}
	devices, err := s.Store.Devices(ctx, scope.OwnerScope)
	if err != nil {
		return nil, err
	}
	environments := []execprotocol.Environment{}
	for _, device := range devices {
		if device.Status != "active" || device.RemovalEpoch != scope.RemovalEpoch || len(device.Grants[agent]) == 0 {
			continue
		}
		environment := device.Environment
		environment.AuthorizationVersion = device.Version
		environment.Capabilities = slices.Clone(device.Grants[agent])
		environment.JournalID = ""
		environments = append(environments, environment)
	}
	return environments, nil
}

func (s *Service) Submit(ctx context.Context, actor, tenant, environment string, request execprotocol.Request, wait time.Duration) (Operation, error) {
	return s.submit(ctx, actor, tenant, environment, request, wait, nil)
}

func (s *Service) SubmitFenced(ctx context.Context, actor, tenant, environment string, request execprotocol.Request, wait time.Duration, fence execprotocol.AuthorityFence) (Operation, error) {
	if fence.ActorEpoch < 1 || fence.MembershipEpoch < 1 || fence.AgentEpoch < 1 {
		return Operation{}, execprotocol.ErrDenied
	}
	return s.submit(ctx, actor, tenant, environment, request, wait, &fence)
}

func (s *Service) submit(ctx context.Context, actor, tenant, environment string, request execprotocol.Request, wait time.Duration, fence *execprotocol.AuthorityFence) (Operation, error) {
	if err := request.Validate(); err != nil {
		return Operation{}, err
	}
	if wait == 0 {
		wait = 24 * time.Hour
	}
	if wait < time.Minute || wait > 30*24*time.Hour {
		return Operation{}, execprotocol.ErrInvalid
	}
	// Normalize object keys so retried JSON encodings retain the same identity.
	var arguments map[string]json.RawMessage
	if json.Unmarshal(request.Arguments, &arguments) != nil || arguments == nil {
		return Operation{}, execprotocol.ErrInvalid
	}
	request.Arguments, _ = json.Marshal(arguments)
	scope, err := s.Authority.Agent(ctx, actor, tenant, request.AgentID, true)
	if err != nil {
		return Operation{}, err
	}
	if fence != nil && (scope.ActorAuthorizationEpoch != fence.ActorEpoch || scope.MembershipExecutionEpoch != fence.MembershipEpoch || scope.AgentExecutionEpoch != fence.AgentEpoch) {
		return Operation{}, execprotocol.ErrDenied
	}
	device, err := s.Store.Device(ctx, environment)
	if err != nil {
		return Operation{}, err
	}
	if !permits(device, scope, request.Kind) {
		return Operation{}, execprotocol.ErrDenied
	}
	return s.Store.Enqueue(ctx, device, scope, request, wait, fence != nil && request.Kind == "mcp_connect")
}

// AcknowledgeOutput transfers responsibility for persisted notifications to
// Runtime. Device acknowledgment alone only confirms transport into Execution.
func (s *Service) AcknowledgeOutput(ctx context.Context, actor, tenant, agent, environment, id string, cursor int64) error {
	if cursor < 0 {
		return execprotocol.ErrInvalid
	}
	if _, err := s.Operation(ctx, actor, tenant, agent, environment, id, 0, 1); err != nil {
		return err
	}
	return s.Store.AcknowledgeOutput(ctx, environment, id, cursor)
}

func (s *Service) Operation(ctx context.Context, actor, tenant, agent, environment, id string, cursor int64, limit int) (Operation, error) {
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return Operation{}, err
	}
	device, err := s.Store.Device(ctx, environment)
	if err != nil {
		return Operation{}, err
	}
	if device.TenantID != scope.TenantID || device.UserID != scope.UserID || device.FleetID != scope.FleetID {
		return Operation{}, execprotocol.ErrDenied
	}
	operation, err := s.Store.Operation(ctx, environment, id, cursor, limit)
	if err != nil {
		return Operation{}, err
	}
	if operation.Scope.TenantID != scope.TenantID || operation.Scope.UserID != scope.UserID || operation.Scope.FleetID != scope.FleetID || operation.Scope.AgentID != agent {
		return Operation{}, execprotocol.ErrDenied
	}
	return operation, nil
}

func (s *Service) Cancel(ctx context.Context, actor, tenant, agent, environment, id string) error {
	if _, err := s.Operation(ctx, actor, tenant, agent, environment, id, 0, 1); err != nil {
		return err
	}
	return s.Store.CancelOperation(ctx, environment, id)
}

func (s *Service) Extend(ctx context.Context, actor, tenant, agent, environment, id string, wait time.Duration) error {
	operation, err := s.Operation(ctx, actor, tenant, agent, environment, id, 0, 1)
	if err != nil {
		return err
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, true)
	if err != nil {
		return err
	}
	device, err := s.Store.Device(ctx, environment)
	if err != nil {
		return err
	}
	if !permits(device, scope, operation.Request.Kind) || wait < time.Minute || wait > 30*24*time.Hour {
		return execprotocol.ErrDenied
	}
	return s.Store.ExtendWait(ctx, environment, id, wait)
}

// EffectiveGrants is evaluated independently of transport authentication. A
// revoked credential may drain results and cancellations, but execute nothing.
func (s *Service) EffectiveGrants(ctx context.Context, device Device) (map[string][]execprotocol.Capability, error) {
	grants := map[string][]execprotocol.Capability{}
	if device.Status != "active" {
		return grants, nil
	}
	owner, err := s.Authority.Owner(ctx, device.UserID, device.TenantID, device.UserID, true)
	if err != nil {
		return nil, err
	}
	if owner.RemovalEpoch != device.RemovalEpoch || owner.FleetID != device.FleetID {
		if device.Kind == "hosted" {
			return grants, nil
		}
		return grants, s.Store.Revoke(ctx, device.ID, device.UserID)
	}
	for agent, capabilities := range device.Grants {
		scope, err := s.Authority.Agent(ctx, device.UserID, device.TenantID, agent, true)
		if err != nil {
			continue
		}
		if scope.FleetID == device.FleetID {
			grants[agent] = slices.Clone(capabilities)
		}
	}
	return grants, nil
}
