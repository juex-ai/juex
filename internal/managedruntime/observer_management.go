package managedruntime

import (
	"context"
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
)

// ExtensionCatalog is private admission data, independent of model availability.
type ExtensionCatalog struct {
	AgentVersion int64                     `json:"agent_version"`
	Bindings     []extensionpolicy.Binding `json:"bindings"`
}
type ExtensionAuthority interface {
	ExtensionCatalog(context.Context, Scope) (ExtensionCatalog, error)
}

type ObserverStart struct {
	RequestID    string `json:"request_id"`
	ThreadID     string `json:"thread_id"`
	BindingID    string `json:"binding_id"`
	ResourceID   string `json:"resource_id"`
	Kind         string `json:"kind"`
	AgentVersion int64  `json:"agent_version"`
	Revision     string `json:"revision"`
	Mode         string `json:"mode"`
	Subscribe    bool   `json:"subscribe"`
}

func (v ObserverStart) Validate() error {
	if (v.RequestID == "" || len(v.RequestID) > 200 || !utf8.ValidString(v.RequestID)) || uuid.Validate(v.ThreadID) != nil || uuid.Validate(v.BindingID) != nil || v.ResourceID == "" || len(v.ResourceID) > 128 || v.AgentVersion < 1 || v.Revision == "" || len(v.Revision) > 256 || (v.Kind != "mcp" && v.Kind != "observable") || (v.Mode != "once" && v.Mode != "continuous") {
		return ErrInvalid
	}
	return nil
}

type ObserverControl struct {
	RequestID  string    `json:"request_id"`
	ID         string    `json:"id"`
	ThreadID   string    `json:"thread_id"`
	BindingID  string    `json:"binding_id"`
	ResourceID string    `json:"resource_id"`
	Kind       string    `json:"kind"`
	Revision   string    `json:"revision"`
	Mode       string    `json:"mode"`
	Desired    string    `json:"desired"`
	SourceID   string    `json:"source_id"`
	State      string    `json:"state"`
	Attempt    int64     `json:"attempt"`
	CreatedAt  time.Time `json:"created_at"`
}

// ObserverWork never crosses the public inspection boundary: the frozen request may contain credentials.
type ObserverWork struct {
	ObserverControl
	Scope         Scope
	Start         ObserverStart
	EnvironmentID string
	Request       execprotocol.Request
	Admitted      bool
	LeaseEpoch    int64
}
type ObserverOutcome struct {
	State                   string
	Admitted, Stop, Restart bool
}

type ObserverTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ObservationStatus struct {
	SubscriptionsTruncated bool           `json:"subscriptions_truncated"`
	ID                     string         `json:"id"`
	ThreadID               string         `json:"thread_id"`
	Origin                 string         `json:"origin"`
	ControlID              string         `json:"control_id"`
	EnvironmentID          string         `json:"environment_id"`
	OperationID            string         `json:"operation_id"`
	Kind                   string         `json:"kind"`
	Directory              string         `json:"directory"`
	Cursor                 int64          `json:"cursor"`
	Closed                 bool           `json:"closed"`
	StopRequested          bool           `json:"stop_requested"`
	Subscriptions          []Subscription `json:"subscriptions"`
}
type ObservationPage struct {
	Targets          []ObserverTarget    `json:"targets"`
	TargetsTruncated bool                `json:"targets_truncated"`
	Sources          []ObservationStatus `json:"sources"`
	Controls         []ObserverControl   `json:"controls"`
	Next             string              `json:"next"`
}
type ObservedEvent struct {
	Observation
	DataTruncated bool `json:"data_truncated"`
	Pending       int  `json:"pending"`
	Delivered     int  `json:"delivered"`
	Skipped       int  `json:"skipped"`
}
type ObservedEvents struct {
	Items []ObservedEvent `json:"items"`
	Next  string          `json:"next"`
}

type ObserverManagementStore interface {
	ExistingObserver(context.Context, Scope, ObserverStart) (ObserverControl, bool, error)
	StartObserver(context.Context, Scope, ObserverStart, string, execprotocol.Request) (ObserverControl, error)
	StopObserver(context.Context, Scope, string) error
	ObservationSources(context.Context, Scope, string) (ObservationPage, error)
	ObservedEvents(context.Context, Scope, string, string) (ObservedEvents, error)
	ObserverSource(context.Context, Scope, string) (ObservationSource, error)
	CurrentObserverSource(context.Context, Scope, string) (ObservationSource, error)
	SetSourceSubscription(context.Context, Scope, string, string, bool, int64) (Subscription, error)
}
type ObserverWorkerStore interface {
	ClaimObserver(context.Context, string) (ObserverWork, error)
	FinishObserver(context.Context, ObserverWork, ObserverOutcome) error
	ReleaseObserverClaims(context.Context, string) error
	CleanupStoppedSubscription(context.Context) error
}

func (s *Service) StartObserver(ctx context.Context, actor, tenant, agent string, request ObserverStart) (ObserverControl, error) {
	if err := request.Validate(); err != nil {
		return ObserverControl{}, err
	}
	scope, err := s.scope(ctx, actor, tenant, agent, true)
	if err != nil {
		return ObserverControl{}, err
	}
	store, ok := s.Store.(ObserverManagementStore)
	if !ok || s.Tools == nil {
		return ObserverControl{}, ErrInvalid
	}
	if value, found, err := store.ExistingObserver(ctx, scope, request); found || err != nil {
		return value, err
	}
	if !scope.Capabilities.Allows(agentpolicy.Extensions) || !scope.Capabilities.Allows(agentpolicy.Observations) || (request.Kind == "mcp" && !scope.Capabilities.Allows(agentpolicy.MCP)) || (request.Kind == "observable" && !scope.Capabilities.Allows(agentpolicy.Shell)) {
		return ObserverControl{}, ErrDenied
	}
	authority, ok := s.Authority.(ExtensionAuthority)
	if !ok {
		return ObserverControl{}, ErrInvalid
	}
	catalog, err := authority.ExtensionCatalog(ctx, scope)
	if err != nil {
		return ObserverControl{}, err
	}
	if catalog.AgentVersion != request.AgentVersion {
		return ObserverControl{}, ErrConflict
	}
	index := slices.IndexFunc(catalog.Bindings, func(b extensionpolicy.Binding) bool {
		return b.ID == request.BindingID && b.Enabled && b.Catalog.Revision == request.Revision
	})
	if index < 0 {
		return ObserverControl{}, ErrConflict
	}
	environments, err := s.Tools.Environments(ctx, scope)
	if err != nil {
		return ObserverControl{}, err
	}
	environment, prepared, err := prepareBoundResource(uuid.NewString(), scope.AgentID, catalog.Bindings[index], request.Kind, request.ResourceID, environments)
	if err != nil {
		return ObserverControl{}, err
	}
	fresh, err := s.Authority.Authorize(ctx, actor, tenant, agent, true)
	if err != nil {
		return ObserverControl{}, err
	}
	if !scope.SameAuthority(fresh) {
		return ObserverControl{}, ErrDenied
	}
	return store.StartObserver(ctx, scope, request, environment, prepared)
}
func (s *Service) StopObserver(ctx context.Context, actor, tenant, agent, source string) error {
	if uuid.Validate(source) != nil {
		return ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return err
	}
	store, ok := s.Store.(ObserverManagementStore)
	if !ok {
		return ErrInvalid
	}
	return store.StopObserver(ctx, scope, source)
}
func (s *Service) ObservationSources(ctx context.Context, actor, tenant, agent, after string) (ObservationPage, error) {
	if after != "" && uuid.Validate(after) != nil {
		return ObservationPage{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return ObservationPage{}, err
	}
	store, ok := s.Store.(ObserverManagementStore)
	if !ok {
		return ObservationPage{}, ErrInvalid
	}
	return store.ObservationSources(ctx, scope, after)
}
func (s *Service) ObservedEvents(ctx context.Context, actor, tenant, agent, source, after string) (ObservedEvents, error) {
	if uuid.Validate(source) != nil || (after != "" && uuid.Validate(after) != nil) {
		return ObservedEvents{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return ObservedEvents{}, err
	}
	store, ok := s.Store.(ObserverManagementStore)
	if !ok {
		return ObservedEvents{}, ErrInvalid
	}
	return store.ObservedEvents(ctx, scope, source, after)
}
func (s *Service) SetSourceSubscription(ctx context.Context, actor, tenant, agent, source, thread string, enabled bool) (Subscription, error) {
	if uuid.Validate(source) != nil || uuid.Validate(thread) != nil {
		return Subscription{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, enabled)
	if err != nil {
		return Subscription{}, err
	}
	if enabled && !scope.Capabilities.Allows(agentpolicy.Observations) {
		return Subscription{}, ErrDenied
	}
	store, ok := s.Store.(ObserverManagementStore)
	if !ok {
		return Subscription{}, ErrInvalid
	}
	original, err := store.CurrentObserverSource(ctx, scope, source)
	if err != nil {
		return Subscription{}, err
	}
	source = original.ID
	var offset int64
	if enabled {
		if !scope.SameAuthority(original.Scope) || s.Tools == nil {
			return Subscription{}, ErrDenied
		}
		environments, err := s.Tools.Environments(ctx, scope)
		if err != nil {
			return Subscription{}, err
		}
		if !observationGrant(environments, original.EnvironmentID, original.AuthorizationVersion, execprotocol.RequiredCapability(original.Kind)) {
			return Subscription{}, ErrDenied
		}
		operation, err := s.Tools.Operation(ctx, scope, original.EnvironmentID, original.OperationID, 0)
		if err != nil {
			return Subscription{}, err
		}
		offset = operation.Snapshot.OutputBytes
	}
	return store.SetSourceSubscription(ctx, scope, source, thread, enabled, offset)
}

// Request construction is shared by model and human admission; neither invents a Turn for the other.
func prepareBoundResource(id, agent string, binding extensionpolicy.Binding, kind, resource string, environments []execprotocol.Environment) (string, execprotocol.Request, error) {
	var args any
	operation := ""
	switch kind {
	case "mcp":
		for _, command := range binding.Catalog.Manifest.MCP {
			if command.ID != resource || !binding.Selected(kind, resource) {
				continue
			}
			operation = "mcp_connect"
			if command.Kind() == "stdio" {
				args = map[string]any{"command": command.Command[0], "args": command.Command[1:], "working_directory": binding.Directory, "environment": binding.Environment(command.Environment), "extension": binding.Context()}
			} else {
				args = map[string]any{"transport": command.Kind(), "url": command.URL, "headers": command.Headers, "working_directory": binding.Directory, "extension": binding.Context()}
			}
			break
		}
	case "observable":
		for _, command := range binding.Catalog.Manifest.Observables {
			if command.ID == resource && binding.Selected(kind, resource) {
				operation = "observe_command"
				args = execprotocol.ObservableCommand{Command: command.Command, WorkingDirectory: binding.Directory, Environment: binding.Environment(command.Environment), Extension: binding.Context(), Options: command.Options}
				break
			}
		}
	}
	if operation == "" {
		return "", execprotocol.Request{}, ErrInvalid
	}
	if !slices.ContainsFunc(environments, func(e execprotocol.Environment) bool {
		return e.ID == binding.EnvironmentID && e.AuthorizationVersion == binding.AuthorizationVersion && slices.Contains(e.Capabilities, execprotocol.RequiredCapability(operation))
	}) {
		return "", execprotocol.Request{}, ErrDenied
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", execprotocol.Request{}, err
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: id, AgentID: agent, AuthorizationVersion: binding.AuthorizationVersion, Kind: operation, Arguments: encoded}
	return binding.EnvironmentID, request, request.Validate()
}
