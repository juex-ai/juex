package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

type WorkspaceExecution interface {
	Environments(context.Context, string, string, string) ([]execprotocol.Environment, error)
	Submit(context.Context, string, string, string, execprotocol.Request, time.Duration) (execution.Operation, error)
	Operation(context.Context, string, string, string, string, string, int64, int) (execution.Operation, error)
}

type ManagementWorkspace struct {
	Execution WorkspaceExecution
	Directory *managementpg.Directory
}

func (s ManagementWorkspace) Read(ctx context.Context, actor, tenant, agent string, request management.WorkspaceReadRequest) (management.WorkspaceReadReceipt, error) {
	if !path.IsAbs(request.Directory) || len(request.Directory) > 4096 || strings.ContainsRune(request.Directory, 0) || request.Query.Validate() != nil {
		return management.WorkspaceReadReceipt{}, management.ErrInvalid
	}
	environments, err := s.Execution.Environments(ctx, actor, tenant, agent)
	if err != nil {
		return management.WorkspaceReadReceipt{}, err
	}
	index := slices.IndexFunc(environments, func(environment execprotocol.Environment) bool {
		return environment.ID == request.EnvironmentID && slices.Contains(environment.Capabilities, execprotocol.Files)
	})
	if index < 0 {
		return management.WorkspaceReadReceipt{}, management.ErrDenied
	}
	arguments, _ := json.Marshal(map[string]any{"working_directory": request.Directory, "browse": request.Query})
	op, err := s.Execution.Submit(ctx, actor, tenant, request.EnvironmentID, execprotocol.Request{Version: execprotocol.Version, ID: request.RequestID, AgentID: agent, Kind: "browse_workspace", Arguments: arguments, AuthorizationVersion: environments[index].AuthorizationVersion}, time.Minute)
	if err != nil {
		return management.WorkspaceReadReceipt{}, err
	}
	return s.Receipt(ctx, actor, tenant, agent, op.EnvironmentID, op.ID)
}

func (s ManagementWorkspace) Receipt(ctx context.Context, actor, tenant, agent, environment, operation string) (management.WorkspaceReadReceipt, error) {
	environments, err := s.Execution.Environments(ctx, actor, tenant, agent)
	if err != nil {
		return management.WorkspaceReadReceipt{}, err
	}
	if !slices.ContainsFunc(environments, func(value execprotocol.Environment) bool {
		return value.ID == environment && slices.Contains(value.Capabilities, execprotocol.Files)
	}) {
		return management.WorkspaceReadReceipt{}, management.ErrDenied
	}
	op, err := s.Execution.Operation(ctx, actor, tenant, agent, environment, operation, 0, 64<<10)
	if err != nil {
		return management.WorkspaceReadReceipt{}, err
	}
	return s.receipt(ctx, actor, tenant, agent, op)
}

func (s ManagementWorkspace) receipt(ctx context.Context, actor, tenant, agent string, op execution.Operation) (management.WorkspaceReadReceipt, error) {
	result := management.WorkspaceReadReceipt{OperationID: op.ID, EnvironmentID: op.EnvironmentID, State: op.State, Error: op.Snapshot.Error, AuthorizationVersion: op.Request.AuthorizationVersion, ReadStartedAt: op.CreatedAt}
	if op.Request.Kind != "browse_workspace" || op.Request.AgentID != agent {
		return result, management.ErrDenied
	}
	var args struct {
		WorkingDirectory string                      `json:"working_directory"`
		Browse           execprotocol.WorkspaceQuery `json:"browse"`
	}
	if json.Unmarshal(op.Request.Arguments, &args) != nil {
		return result, management.ErrInvalid
	}
	result.Directory, result.Query = args.WorkingDirectory, args.Browse
	if op.State != "completed" {
		return result, nil
	}
	// The device can finish before Execution has transported all output chunks.
	// Wait for the complete retained receipt instead of decoding a partial JSON.
	if op.ResultCursor < op.Snapshot.OutputBytes {
		result.State = "running"
		return result, nil
	}
	data := slices.Clone(op.Snapshot.Output)
	for {
		if op.Snapshot.OutputExpired || op.Snapshot.Truncated || op.Snapshot.OutputBytes > execprotocol.WorkspaceListingBytes {
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
	var listing execprotocol.WorkspaceListing
	if json.Unmarshal(data, &listing) != nil {
		return result, management.ErrInvalid
	}
	result.Listing = &listing
	return result, nil
}

func (s ManagementWorkspace) PreviewConfiguration(ctx context.Context, actor, tenant, agent, environment, operation string) (management.WorkspaceConfigurationPreview, error) {
	receipt, err := s.Receipt(ctx, actor, tenant, agent, environment, operation)
	result := management.WorkspaceConfigurationPreview{Receipt: receipt}
	if err != nil {
		return result, err
	}
	if receipt.State != "completed" {
		return result, nil
	}
	if !receipt.Query.Read || receipt.Listing == nil || receipt.Listing.Preview == nil {
		return result, management.ErrInvalid
	}
	preview := receipt.Listing.Preview
	if preview.Binary || preview.Truncated || preview.Entry.Kind != "file" || preview.Entry.Path != receipt.Query.Path || preview.Entry.Size != int64(len(preview.Text)) {
		return result, management.ErrInvalid
	}
	models, err := s.Directory.Models(ctx, actor, tenant)
	if err != nil {
		return result, err
	}
	declaration, err := management.ParseWorkspaceConfiguration([]byte(preview.Text), models)
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256([]byte(preview.Text))
	authority, err := s.Directory.ReadAgent(ctx, actor, tenant, agent)
	if err != nil {
		return result, err
	}
	version := int64(1)
	if authority.Agent.WorkspaceConfiguration != nil {
		version = authority.Agent.WorkspaceConfiguration.Version + 1
	}
	snapshot := management.WorkspaceConfiguration{ConfigurationLayer: management.ConfigurationLayer{Version: version, Declaration: declaration}, EnvironmentID: receipt.EnvironmentID, WorkingDirectory: receipt.Directory, Path: receipt.Query.Path, OperationID: receipt.OperationID, SHA256: hex.EncodeToString(hash[:]), AuthorizationVersion: receipt.AuthorizationVersion, ReadStartedAt: receipt.ReadStartedAt}
	authority.Layers.Workspace = snapshot.ConfigurationLayer
	effective := authority.Layers.Resolve()
	// A configuration preview exposes typed declarations, never arbitrary file
	// contents such as an accidentally selected credential document.
	result.Receipt.Listing = nil
	result.Configuration, result.Effective = &snapshot, &effective
	return result, nil
}

func (s ManagementWorkspace) Configure(ctx context.Context, actor, tenant, agent string, change management.WorkspaceConfigurationChange) (management.Agent, error) {
	if change.Remove {
		return s.Directory.ConfigureWorkspace(ctx, actor, tenant, agent, change.Version, nil)
	}
	preview, err := s.PreviewConfiguration(ctx, actor, tenant, agent, change.EnvironmentID, change.OperationID)
	if err != nil {
		return management.Agent{}, err
	}
	if preview.Configuration == nil || preview.Configuration.SHA256 != change.SHA256 {
		return management.Agent{}, management.ErrConflict
	}
	environments, err := s.Execution.Environments(ctx, actor, tenant, agent)
	if err != nil {
		return management.Agent{}, err
	}
	snapshot := preview.Configuration
	if !slices.ContainsFunc(environments, func(environment execprotocol.Environment) bool {
		return environment.ID == snapshot.EnvironmentID && environment.AuthorizationVersion == snapshot.AuthorizationVersion && slices.Contains(environment.Capabilities, execprotocol.Files)
	}) {
		return management.Agent{}, management.ErrDenied
	}
	return s.Directory.ConfigureWorkspace(ctx, actor, tenant, agent, change.Version, snapshot)
}
