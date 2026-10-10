package execution

import (
	"context"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type EnvironmentStatus struct {
	execprotocol.Environment
	Managed  bool       `json:"managed"`
	LastSeen *time.Time `json:"last_seen"`
}

// EnvironmentInspection describes persisted bindings and presence. Reading it
// never provisions an environment, wakes an executor, or renews presence.
type EnvironmentInspection struct {
	Binding      DefaultEnvironment  `json:"binding"`
	DefaultState string              `json:"default_state"`
	Environments []EnvironmentStatus `json:"environments"`
	ObservedAt   time.Time           `json:"observed_at"`
}

type EnvironmentInspectionStore interface {
	InspectEnvironments(context.Context, Scope) (DefaultEnvironment, []Device, time.Time, error)
}

func (s *Service) InspectEnvironments(ctx context.Context, actor, tenant, agent string) (EnvironmentInspection, error) {
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return EnvironmentInspection{}, err
	}
	store, ok := s.Store.(EnvironmentInspectionStore)
	if !ok {
		return EnvironmentInspection{}, execprotocol.ErrInvalid
	}
	binding, devices, observed, err := store.InspectEnvironments(ctx, scope)
	if err != nil {
		return EnvironmentInspection{}, err
	}
	result := EnvironmentInspection{Binding: binding, DefaultState: "unprovisioned", Environments: []EnvironmentStatus{}, ObservedAt: observed}
	if binding.EnvironmentID != "" {
		result.DefaultState = "unavailable"
	}
	for _, device := range devices {
		environment, visible := projectEnvironment(device, scope, binding)
		if !visible {
			continue
		}
		if environment.Default {
			result.DefaultState = "selected"
			if binding.EnvironmentID == "" {
				result.DefaultState = "automatic"
			}
		}
		result.Environments = append(result.Environments, EnvironmentStatus{Environment: environment, Managed: device.Managed, LastSeen: device.LastSeen})
	}
	return result, nil
}

func projectEnvironment(device Device, scope Scope, binding DefaultEnvironment) (execprotocol.Environment, bool) {
	if device.Status != "active" || device.TenantID != scope.TenantID || device.UserID != scope.UserID || device.FleetID != scope.FleetID || device.RemovalEpoch != scope.RemovalEpoch || len(device.Grants[scope.AgentID]) == 0 {
		return execprotocol.Environment{}, false
	}
	environment := device.Environment
	environment.Default = device.ID == binding.EnvironmentID || binding.EnvironmentID == "" && device.Managed
	if environment.Default && binding.WorkingDirectory != "" {
		environment.WorkingDirectory = binding.WorkingDirectory
	}
	environment.AuthorizationVersion = device.Version
	environment.Capabilities = permittedCapabilities(scope.Capabilities, device.Grants[scope.AgentID])
	environment.JournalID = ""
	return environment, true
}
