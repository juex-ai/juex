package managed

import (
	"context"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	managementwire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/management"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

// ExecutionAuthority translates private Management contracts at the composition
// boundary. Execution never imports Management storage or model providers.
type ExecutionAuthority struct{ client managementwire.Client }

func NewExecutionAuthority(address string, credentials platformrpc.Credentials) (*ExecutionAuthority, error) {
	options, err := platformrpc.ClientOptions(address, "management", credentials)
	if err != nil {
		return nil, err
	}
	client, err := managementwire.NewClient("management", options...)
	if err != nil {
		return nil, err
	}
	return &ExecutionAuthority{client: client}, nil
}

func executionCode(code string) error {
	if code == "unavailable" {
		return platformrpc.ErrUnavailable
	}
	return execprotocol.FromErrorCode(code)
}

func (a *ExecutionAuthority) Owner(ctx context.Context, actor, tenant, owner string, execute bool) (execution.OwnerScope, error) {
	reply, err := a.client.AuthorizeFleet(ctx, actor, tenant, owner, execute)
	var authority management.FleetAuthority
	if err := platformrpc.Decode(reply, err, &authority, executionCode); err != nil {
		return execution.OwnerScope{}, err
	}
	return execution.OwnerScope{TenantID: authority.Fleet.TenantID, UserID: authority.Fleet.UserID, FleetID: authority.Fleet.ID, ActorID: authority.ActorID, ActorAuthorizationEpoch: authority.ActorAuthorizationEpoch, MembershipExecutionEpoch: authority.MembershipExecutionEpoch, RemovalEpoch: authority.RemovalEpoch, CanExecute: authority.CanExecute, OwnerEmail: authority.OwnerEmail, TenantName: authority.TenantName}, nil
}

func (a *ExecutionAuthority) Agent(ctx context.Context, actor, tenant, agent string, execute bool) (execution.Scope, error) {
	actorRef := &platform.Actor{UserID: actor, TenantID: tenant, AgentID: agent}
	reply, err := a.client.Authorize(ctx, actorRef, execute)
	var authority managedruntime.Scope
	if err := platformrpc.Decode(reply, err, &authority, executionCode); err != nil {
		return execution.Scope{}, err
	}
	owner, err := a.Owner(ctx, actor, tenant, authority.UserID, execute)
	if err != nil {
		return execution.Scope{}, err
	}
	if owner.FleetID != authority.FleetID || owner.MembershipExecutionEpoch != authority.MembershipExecutionEpoch || owner.ActorAuthorizationEpoch != authority.ActorAuthorizationEpoch {
		return execution.Scope{}, execprotocol.ErrDenied
	}
	return execution.Scope{Capabilities: authority.Capabilities, OwnerScope: owner, AgentID: authority.AgentID, AgentExecutionEpoch: authority.AgentExecutionEpoch}, nil
}
