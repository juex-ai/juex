package managed

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/execution"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

type ManagementExtensions struct {
	Directory *managementpg.Directory
	Execution *executionrpc.Client
}

func (s ManagementExtensions) Inspect(ctx context.Context, actor, tenant, agent string, request management.ExtensionInspectionRequest) (management.ExtensionInspection, error) {
	if !path.IsAbs(request.Directory) || len(request.Directory) > 4096 || strings.ContainsRune(request.Directory, 0) || request.SourceKind != "" && request.SourceKind != "skills" {
		return management.ExtensionInspection{}, management.ErrInvalid
	}
	envs, err := s.Execution.Environments(ctx, actor, tenant, agent)
	if err != nil {
		return management.ExtensionInspection{}, err
	}
	index := slices.IndexFunc(envs, func(e execprotocol.Environment) bool {
		return e.ID == request.EnvironmentID && slices.Contains(e.Capabilities, execprotocol.Files)
	})
	if index < 0 {
		return management.ExtensionInspection{}, management.ErrDenied
	}
	arguments, _ := json.Marshal(struct {
		Path       string `json:"path"`
		SourceKind string `json:"source_kind,omitempty"`
	}{request.Directory, request.SourceKind})
	op, err := s.Execution.Submit(ctx, actor, tenant, request.EnvironmentID, execprotocol.Request{Version: execprotocol.Version, ID: request.RequestID, AgentID: agent, Kind: "inspect_extension", Arguments: arguments, AuthorizationVersion: envs[index].AuthorizationVersion}, 0)
	if err != nil {
		return management.ExtensionInspection{}, err
	}
	return s.inspection(ctx, actor, tenant, agent, op)
}
func (s ManagementExtensions) Inspection(ctx context.Context, actor, tenant, agent, environment, id string) (management.ExtensionInspection, error) {
	op, err := s.Execution.Operation(ctx, actor, tenant, agent, environment, id, 0, 64<<10)
	if err != nil {
		return management.ExtensionInspection{}, err
	}
	return s.inspection(ctx, actor, tenant, agent, op)
}
func (s ManagementExtensions) inspection(ctx context.Context, actor, tenant, agent string, op execution.Operation) (management.ExtensionInspection, error) {
	result := management.ExtensionInspection{OperationID: op.ID, EnvironmentID: op.EnvironmentID, AuthorizationVersion: op.Request.AuthorizationVersion, State: op.State, Error: op.Snapshot.Error}
	if op.Request.Kind != "inspect_extension" || op.Request.AgentID != agent {
		return result, management.ErrDenied
	}
	var arguments struct {
		Path       string `json:"path"`
		SourceKind string `json:"source_kind,omitempty"`
	}
	if json.Unmarshal(op.Request.Arguments, &arguments) != nil {
		return result, management.ErrInvalid
	}
	result.Directory = arguments.Path
	result.SourceKind = arguments.SourceKind
	if op.State != "completed" {
		return result, nil
	}
	data := slices.Clone(op.Snapshot.Output)
	for {
		if op.Snapshot.OutputExpired || op.Snapshot.Truncated || op.Snapshot.OutputBytes > extensionpolicy.MaxCatalogBytes+1 {
			return result, management.ErrInvalid
		}
		if op.Snapshot.NextCursor >= op.Snapshot.OutputBytes {
			break
		}
		previous := op.Snapshot.NextCursor
		var err error
		op, err = s.Execution.Operation(ctx, actor, tenant, agent, op.EnvironmentID, op.ID, previous, 64<<10)
		if err != nil {
			return result, err
		}
		if op.Snapshot.NextCursor <= previous {
			return result, management.ErrInvalid
		}
		data = append(data, op.Snapshot.Output...)
	}
	var catalog extensionpolicy.Catalog
	if json.Unmarshal(data, &catalog) != nil || catalog.Validate() != nil || catalog.SourceKind != arguments.SourceKind {
		return result, management.ErrInvalid
	}
	result.Catalog = &catalog
	return result, nil
}
func (s ManagementExtensions) Configure(ctx context.Context, actor, tenant, agent, id string, change management.ExtensionChange) (management.Agent, error) {
	authority, err := s.Directory.ReadAgent(ctx, actor, tenant, agent)
	if err != nil {
		return management.Agent{}, err
	}
	bindings := authority.Agent.Extensions
	at := slices.IndexFunc(bindings, func(b extensionpolicy.Binding) bool { return b.ID == id })
	binding := extensionpolicy.Binding{ID: id}
	if change.Remove {
		return s.Directory.ConfigureExtension(ctx, actor, tenant, agent, change.Version, binding, true, nil)
	}
	if change.InspectionID == "" {
		if at < 0 {
			return management.Agent{}, management.ErrInvalid
		}
		binding = bindings[at]
	} else {
		receipt, err := s.Inspection(ctx, actor, tenant, agent, change.EnvironmentID, change.InspectionID)
		if err != nil {
			return management.Agent{}, err
		}
		if receipt.State != "completed" || receipt.Catalog == nil {
			return management.Agent{}, management.ErrConflict
		}
		envs, err := s.Execution.Environments(ctx, actor, tenant, agent)
		if err != nil {
			return management.Agent{}, err
		}
		if !slices.ContainsFunc(envs, func(e execprotocol.Environment) bool {
			return e.ID == receipt.EnvironmentID && e.AuthorizationVersion == receipt.AuthorizationVersion && slices.Contains(e.Capabilities, execprotocol.Files)
		}) {
			return management.Agent{}, management.ErrDenied
		}
		binding = extensionpolicy.Binding{ID: id, EnvironmentID: receipt.EnvironmentID, AuthorizationVersion: receipt.AuthorizationVersion, Directory: receipt.Directory, InspectionID: receipt.OperationID, Catalog: *receipt.Catalog}
	}
	binding.Enabled, binding.Resources = change.Enabled, slices.Clone(change.Resources)
	var inspection *management.ExtensionInspectionAuthority
	if binding.Enabled && binding.Catalog.SourceKind == "skills" {
		op, err := s.Execution.Operation(ctx, actor, tenant, agent, binding.EnvironmentID, binding.InspectionID, 0, 1)
		if err != nil {
			return management.Agent{}, err
		}
		if op.Request.Kind != "inspect_extension" || op.State != "completed" || op.Request.AuthorizationVersion != binding.AuthorizationVersion {
			return management.Agent{}, management.ErrDenied
		}
		inspection = &management.ExtensionInspectionAuthority{ActorID: op.Scope.ActorID, ActorEpoch: op.Scope.ActorAuthorizationEpoch, MembershipEpoch: op.Scope.MembershipExecutionEpoch, AgentEpoch: op.Scope.AgentExecutionEpoch}
		envs, err := s.Execution.Environments(ctx, actor, tenant, agent)
		if err != nil {
			return management.Agent{}, err
		}
		if !slices.ContainsFunc(envs, func(e execprotocol.Environment) bool {
			return e.ID == binding.EnvironmentID && e.AuthorizationVersion == binding.AuthorizationVersion && slices.Contains(e.Capabilities, execprotocol.Files)
		}) {
			return management.Agent{}, management.ErrDenied
		}
	}
	return s.Directory.ConfigureExtension(ctx, actor, tenant, agent, change.Version, binding, false, inspection)
}
