package managed

import (
	"context"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
	"github.com/juex-ai/juex/internal/providers"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

type RuntimeAuthority struct{ Directory *postgres.Directory }

func (a RuntimeAuthority) AuthorizeFleet(ctx context.Context, actor, tenant, owner string, execute bool) (management.FleetAuthority, error) {
	value, err := a.Directory.AuthorizeFleet(ctx, actor, tenant, owner, execute)
	return value, runtimeError(err)
}

func runtimeScope(a management.AgentAuthority) managedruntime.Scope {
	return managedruntime.Scope{TenantID: a.Fleet.TenantID, UserID: a.Fleet.UserID, FleetID: a.Fleet.ID, AgentID: a.Agent.ID, ActorID: a.ActorID,
		ActorAuthorizationEpoch: a.ActorAuthorizationEpoch, MembershipVersion: a.MembershipVersion, MembershipExecutionEpoch: a.MembershipExecutionEpoch, AgentExecutionEpoch: a.Agent.ExecutionEpoch}
}

func runtimeError(err error) error {
	if errors.Is(err, management.ErrModelUnavailable) {
		return managedruntime.ErrModelUnavailable
	}
	if errors.Is(err, management.ErrDenied) {
		return managedruntime.ErrDenied
	}
	if errors.Is(err, management.ErrInvalid) {
		return managedruntime.ErrInvalid
	}
	return err
}

func (a RuntimeAuthority) Authorize(ctx context.Context, actor, tenant, agent string, execute bool) (managedruntime.Scope, error) {
	var authority management.AgentAuthority
	var err error
	if execute {
		authority, err = a.Directory.AuthorizeAgent(ctx, actor, tenant, agent)
	} else {
		authority, err = a.Directory.ReadAgent(ctx, actor, tenant, agent)
	}
	return runtimeScope(authority), runtimeError(err)
}

func (a RuntimeAuthority) Snapshot(ctx context.Context, scope managedruntime.Scope) (managedruntime.TurnConfig, error) {
	plan, err := a.Directory.SnapshotPlan(ctx, modelScope(scope))
	if err != nil {
		return managedruntime.TurnConfig{}, runtimeError(err)
	}
	config := managedruntime.TurnConfig{AgentVersion: plan.AgentVersion, Instructions: plan.Instructions, RequestedModelID: plan.RequestedModelID}
	for _, candidate := range plan.Candidates {
		config.Models = append(config.Models, managedruntime.ModelConfig(candidate))
	}
	return config, nil
}

func (a RuntimeAuthority) Provider(ctx context.Context, scope managedruntime.Scope, config managedruntime.ModelConfig) (llm.Provider, error) {
	profile, err := a.Profile(ctx, scope, config)
	if err != nil {
		return nil, err
	}
	return providers.NewProvider(profile)
}

func (a RuntimeAuthority) Profile(ctx context.Context, scope managedruntime.Scope, config managedruntime.ModelConfig) (llm.ProviderProfile, error) {
	key, err := a.Directory.ResolveCandidate(ctx, modelScope(scope), management.ModelCandidate(config))
	if err != nil {
		return llm.ProviderProfile{}, runtimeError(err)
	}
	profile, err := providerprofile.ResolveProfile(providerprofile.Config{ID: "managed-" + config.ModelID, Protocol: string(config.Protocol), BaseURL: config.Endpoint, APIKey: key, Model: config.Model})
	if err != nil {
		return llm.ProviderProfile{}, managedruntime.ErrModelUnavailable
	}
	return profile, nil
}

func modelScope(scope managedruntime.Scope) management.ModelCallScope {
	return management.ModelCallScope{ActorID: scope.ActorID, TenantID: scope.TenantID, AgentID: scope.AgentID, UserID: scope.UserID, FleetID: scope.FleetID, ActorAuthorizationEpoch: scope.ActorAuthorizationEpoch, MembershipExecutionEpoch: scope.MembershipExecutionEpoch, AgentExecutionEpoch: scope.AgentExecutionEpoch}
}
