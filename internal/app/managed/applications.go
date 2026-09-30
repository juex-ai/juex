package managed

import (
	"context"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/management"
)

func (a RuntimeAuthority) AuthorizeApplication(ctx context.Context, access application.Access, execute bool) (application.Scope, error) {
	var scope application.Scope
	if access.AgentID != "" {
		var value management.AgentAuthority
		var err error
		if execute {
			value, err = a.Directory.AuthorizeAgent(ctx, access.ActorID, access.TenantID, access.AgentID)
		} else {
			value, err = a.Directory.ReadAgent(ctx, access.ActorID, access.TenantID, access.AgentID)
		}
		if err != nil {
			return scope, applicationError(err)
		}
		if access.UserID != "" && access.UserID != value.Fleet.UserID {
			return scope, application.ErrDenied
		}
		scope = application.Scope{Access: application.Access{ActorID: access.ActorID, TenantID: access.TenantID, UserID: value.Fleet.UserID, AgentID: access.AgentID}, FleetID: value.Fleet.ID, ActorEpoch: value.ActorAuthorizationEpoch, MemberVersion: value.MembershipVersion, MemberEpoch: value.MembershipExecutionEpoch, AgentEpoch: value.Agent.ExecutionEpoch}
	} else {
		value, err := a.Directory.AuthorizeFleet(ctx, access.ActorID, access.TenantID, access.UserID, execute)
		if err != nil {
			return scope, applicationError(err)
		}
		scope = application.Scope{Access: access, FleetID: value.Fleet.ID, ActorEpoch: value.ActorAuthorizationEpoch, MemberVersion: value.MembershipVersion, MemberEpoch: value.MembershipExecutionEpoch}
	}
	return scope, nil
}

func applicationError(err error) error {
	switch {
	case errors.Is(err, management.ErrDenied):
		return application.ErrDenied
	case errors.Is(err, management.ErrInvalid):
		return application.ErrInvalid
	case errors.Is(err, management.ErrConflict):
		return application.ErrConflict
	default:
		return err
	}
}
