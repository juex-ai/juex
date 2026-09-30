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

func runtimeScope(a management.AgentAuthority) managedruntime.Scope {
	return managedruntime.Scope{TenantID: a.Fleet.TenantID, UserID: a.Fleet.UserID, FleetID: a.Fleet.ID, AgentID: a.Agent.ID, ActorID: a.ActorID,
		ActorAuthorizationEpoch: a.ActorAuthorizationEpoch, MembershipVersion: a.MembershipVersion, MembershipExecutionEpoch: a.MembershipExecutionEpoch, AgentExecutionEpoch: a.Agent.ExecutionEpoch}
}

func runtimeError(err error) error {
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
	authority, err := a.Directory.AuthorizeAgent(ctx, scope.ActorID, scope.TenantID, scope.AgentID)
	if err != nil {
		return managedruntime.TurnConfig{}, runtimeError(err)
	}
	if !scope.SameAuthority(runtimeScope(authority)) {
		return managedruntime.TurnConfig{}, managedruntime.ErrDenied
	}
	if authority.ModelID == "" {
		return managedruntime.TurnConfig{}, managedruntime.ErrModelUnavailable
	}
	model, err := a.Directory.ResolveModel(ctx, authority.ModelID)
	if errors.Is(err, management.ErrDenied) {
		return managedruntime.TurnConfig{}, managedruntime.ErrModelUnavailable
	}
	if err != nil {
		return managedruntime.TurnConfig{}, err
	}
	return managedruntime.TurnConfig{AgentVersion: authority.Agent.Version, Instructions: authority.Agent.Instructions, ModelID: model.Model.ID, Provider: model.Model.Provider, Model: model.Model.Name, Protocol: model.Model.Protocol, Endpoint: model.Endpoint, ContextWindow: model.Model.ContextWindow, MaxOutput: model.Model.MaxOutput}, nil
}

func (a RuntimeAuthority) Provider(ctx context.Context, scope managedruntime.Scope, config managedruntime.TurnConfig) (llm.Provider, error) {
	profile, err := a.Profile(ctx, scope, config)
	if err != nil {
		return nil, err
	}
	return providers.NewProvider(profile)
}

func (a RuntimeAuthority) Profile(ctx context.Context, scope managedruntime.Scope, config managedruntime.TurnConfig) (llm.ProviderProfile, error) {
	fresh, err := a.Authorize(ctx, scope.ActorID, scope.TenantID, scope.AgentID, true)
	if err != nil {
		return llm.ProviderProfile{}, err
	}
	if !scope.SameAuthority(fresh) {
		return llm.ProviderProfile{}, managedruntime.ErrDenied
	}
	resolved, err := a.Directory.ResolveModel(ctx, config.ModelID)
	if errors.Is(err, management.ErrDenied) {
		return llm.ProviderProfile{}, managedruntime.ErrModelUnavailable
	}
	if err != nil {
		return llm.ProviderProfile{}, err
	}
	// Never send a rotated credential to an old endpoint after an operator
	// changes routing. The next explicitly submitted Turn takes a new snapshot.
	if resolved.Endpoint != config.Endpoint || resolved.Model.Protocol != config.Protocol || resolved.Model.Provider != config.Provider || resolved.Model.Name != config.Model {
		return llm.ProviderProfile{}, managedruntime.ErrModelUnavailable
	}
	profile, err := providerprofile.ResolveProfile(providerprofile.Config{ID: "managed-" + config.ModelID, Protocol: string(config.Protocol), BaseURL: config.Endpoint, APIKey: resolved.APIKey, Model: config.Model})
	if err != nil {
		return llm.ProviderProfile{}, managedruntime.ErrModelUnavailable
	}
	return profile, nil
}
