package managedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

// These are Runtime's narrow file-service ports. Execution owns artifact
// authorization, durable transfer coordination and all file bytes.
type FileLocation struct {
	EnvironmentID        string `json:"environment_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
	Path                 string `json:"path"`
	WorkingDirectory     string `json:"working_directory,omitempty"`
}

type FileTransferSpec struct {
	Source     *FileLocation `json:"source,omitempty"`
	Target     *FileLocation `json:"target,omitempty"`
	ArtifactID string        `json:"artifact_id,omitempty"`
	Name       string        `json:"name,omitempty"`
	MediaType  string        `json:"media_type,omitempty"`
	Visibility string        `json:"visibility,omitempty"`
}

type FileTransferResult struct {
	ID              string             `json:"transfer_id"`
	State           execprotocol.State `json:"state"`
	WaitReason      string             `json:"wait_reason,omitempty"`
	ArtifactID      string             `json:"artifact_id,omitempty"`
	Error           string             `json:"error,omitempty"`
	CancelRequested bool               `json:"cancel_requested"`
	WaitUntil       time.Time          `json:"wait_until"`
}

type FileArtifact struct {
	ID                  string                    `json:"artifact_id"`
	Name                string                    `json:"name"`
	MediaType           string                    `json:"media_type"`
	Visibility          string                    `json:"visibility"`
	State               string                    `json:"state"`
	Manifest            execprotocol.FileManifest `json:"manifest"`
	SourceEnvironmentID string                    `json:"source_environment_id,omitempty"`
}

type FileGateway interface {
	StartFileTransfer(context.Context, Scope, string, FileTransferSpec) (FileTransferResult, error)
	FileTransfer(context.Context, Scope, string) (FileTransferResult, error)
	CancelFileTransfer(context.Context, Scope, string) error
	CancelFileRequest(context.Context, Scope, string) (execprotocol.State, error)
	FileArtifacts(context.Context, Scope, string, int) ([]FileArtifact, error)
}

func fileCreationTool(name string) bool {
	return name == "publish_file" || name == "import_file" || name == "copy_file"
}

type fileToolLocation struct {
	EnvironmentID    string `json:"environment_id"`
	Path             string `json:"path"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

func prepareFileTransfer(work ToolWork, environments []execprotocol.Environment) (string, execprotocol.Request, error) {
	var args struct {
		Source     *fileToolLocation `json:"source"`
		Target     *fileToolLocation `json:"target"`
		ArtifactID string            `json:"artifact_id"`
		Name       string            `json:"name"`
		MediaType  string            `json:"media_type"`
		Visibility string            `json:"visibility"`
	}
	encoded, err := json.Marshal(work.Call.Input)
	if err != nil {
		return "", execprotocol.Request{}, execprotocol.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil {
		return "", execprotocol.Request{}, execprotocol.ErrInvalid
	}
	resolve := func(location *fileToolLocation) (*FileLocation, error) {
		if location == nil {
			return nil, nil
		}
		if location.Path == "" {
			return nil, execprotocol.ErrInvalid
		}
		for _, environment := range environments {
			if (environment.ID == location.EnvironmentID || location.EnvironmentID == "" && environment.Kind == "hosted") && slices.Contains(environment.Capabilities, execprotocol.Files) {
				return &FileLocation{EnvironmentID: environment.ID, AuthorizationVersion: environment.AuthorizationVersion, Path: location.Path, WorkingDirectory: location.WorkingDirectory}, nil
			}
		}
		return nil, execprotocol.ErrDenied
	}
	spec := FileTransferSpec{ArtifactID: strings.TrimPrefix(args.ArtifactID, "artifact:"), Name: args.Name, MediaType: args.MediaType, Visibility: args.Visibility}
	if spec.Source, err = resolve(args.Source); err != nil {
		return "", execprotocol.Request{}, err
	}
	if spec.Target, err = resolve(args.Target); err != nil {
		return "", execprotocol.Request{}, err
	}
	switch work.Call.ToolName {
	case "publish_file":
		if spec.Source == nil || spec.Target != nil || spec.ArtifactID != "" {
			return "", execprotocol.Request{}, execprotocol.ErrInvalid
		}
	case "copy_file":
		if spec.Source == nil || spec.Target == nil || spec.ArtifactID != "" {
			return "", execprotocol.Request{}, execprotocol.ErrInvalid
		}
	case "import_file":
		if spec.Source != nil || spec.Target == nil || spec.ArtifactID == "" || args.Name != "" || args.MediaType != "" || args.Visibility != "" {
			return "", execprotocol.Request{}, execprotocol.ErrInvalid
		}
	default:
		return "", execprotocol.Request{}, execprotocol.ErrInvalid
	}
	location := spec.Target
	if spec.Source != nil {
		location = spec.Source
		if spec.Name == "" {
			spec.Name = path.Base(spec.Source.Path)
		}
		if spec.MediaType == "" {
			spec.MediaType = "application/octet-stream"
		}
		if spec.Visibility == "" {
			spec.Visibility = "agent"
		}
	}
	encoded, err = json.Marshal(spec)
	return location.EnvironmentID, execprotocol.Request{Version: execprotocol.Version, ID: work.ID, AgentID: work.Scope.AgentID, Kind: work.Call.ToolName, Arguments: encoded}, err
}

func (r toolRunner) fileTool(ctx context.Context, work *ToolWork) (ToolOutcome, bool) {
	name := work.Call.ToolName
	if !fileCreationTool(name) && name != "list_artifacts" && name != "file_transfer_status" && name != "file_transfer_cancel" {
		return ToolOutcome{}, false
	}
	if r.files == nil {
		return toolResult(work.Call, map[string]string{"error": "file service unavailable"}, true), true
	}
	if name == "list_artifacts" {
		after, _ := work.Call.Input["after"].(string)
		values, err := r.files.FileArtifacts(ctx, work.Scope, after, 20)
		if err != nil {
			return executionFailure(err), true
		}
		next := ""
		if len(values) == 20 {
			next = values[len(values)-1].ID
		}
		return toolResult(work.Call, map[string]any{"artifacts": values, "next_after": next}, false), true
	}
	if name == "file_transfer_status" || name == "file_transfer_cancel" {
		id, _ := work.Call.Input["transfer_id"].(string)
		if id == "" {
			return executionFailure(execprotocol.ErrInvalid), true
		}
		if name == "file_transfer_cancel" {
			if err := r.files.CancelFileTransfer(ctx, work.Scope, id); err != nil {
				return executionFailure(err), true
			}
		}
		result, err := r.files.FileTransfer(ctx, work.Scope, id)
		if err != nil {
			return executionFailure(err), true
		}
		return toolResult(work.Call, result, result.State == execprotocol.Failed || result.State == execprotocol.Unknown), true
	}
	if work.Request.ID == "" {
		environments, err := r.gateway.Environments(ctx, work.Scope)
		if err != nil {
			return retryTool(), true
		}
		environment, request, err := prepareFileTransfer(*work, environments)
		if err != nil {
			return executionFailure(err), true
		}
		if err := r.store.PrepareTool(ctx, *work, environment, request); err != nil {
			return retryTool(), true
		}
		work.EnvironmentID, work.Request = environment, request
	}
	var spec FileTransferSpec
	if json.Unmarshal(work.Request.Arguments, &spec) != nil {
		return executionFailure(execprotocol.ErrInvalid), true
	}
	result, err := r.files.StartFileTransfer(ctx, work.Scope, work.ID, spec)
	if err != nil {
		return executionFailure(err), true
	}
	if result.State == execprotocol.Unknown {
		outcome := toolResult(work.Call, result, true)
		outcome.State, outcome.OperationLive = "unknown", true
		return outcome, true
	}
	if result.State.Terminal() {
		return toolResult(work.Call, result, result.State != execprotocol.Completed), true
	}
	reason := "execution"
	if result.WaitReason == "environment" {
		reason = "environment"
	}
	return ToolOutcome{State: "waiting", OperationLive: true, WaitReason: reason}, true
}

func (r toolRunner) cancelFileWork(ctx context.Context, work ToolWork) ToolOutcome {
	if work.Request.ID != "" {
		if r.files == nil {
			return retryTool()
		}
		state, err := r.files.CancelFileRequest(ctx, work.Scope, work.ID)
		if err != nil {
			return retryTool()
		}
		return cancellationOutcome(state)
	}
	return ToolOutcome{State: "cancelled"}
}

func fileTools() []llm.ToolSpec {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	location := map[string]any{"type": "object", "properties": map[string]any{"environment_id": str("Authorized environment ID. Omit only for the hosted workspace. Offline devices wait; never substitute."), "path": str("File path on this environment"), "working_directory": str("Optional absolute working directory")}, "required": []string{"path"}, "additionalProperties": false}
	tool := func(name, description string, properties map[string]any, required ...string) llm.ToolSpec {
		if required == nil {
			required = []string{}
		}
		return llm.ToolSpec{Name: name, Description: description, Schema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
	}
	metadata := func(properties map[string]any) map[string]any {
		properties["name"] = str("Artifact filename; defaults to the source basename")
		properties["media_type"] = str("Optional MIME type; defaults to application/octet-stream")
		properties["visibility"] = map[string]any{"type": "string", "enum": []string{"agent", "fleet"}, "description": "Defaults to private agent. fleet explicitly shares with Agents of the same owner and Fleet."}
		return properties
	}
	return []llm.ToolSpec{
		tool("publish_file", "Publish an immutable file snapshot as an Artifact. Binary bytes never enter model context. Waits durably for offline environments; never repeat an unknown transfer.", metadata(map[string]any{"source": location}), "source"),
		tool("import_file", "Import a ready Artifact into an authorized environment. Verifies all bytes and creates a new path; never overwrites an existing path.", map[string]any{"artifact_id": str("Artifact ID or artifact: reference"), "target": location}, "artifact_id", "target"),
		tool("copy_file", "Explicitly copy between authorized environments through an immutable Artifact. Source and target paths are independent; no automatic synchronization. Target must not exist.", metadata(map[string]any{"source": location, "target": location}), "source", "target"),
		tool("list_artifacts", "List this Agent's file metadata and ready shared Fleet artifacts. Import an Artifact to an environment before using file tools; bytes are not returned here.", map[string]any{"after": str("Optional pagination cursor from next_after")}),
		tool("file_transfer_status", "Inspect an original transfer without repeating its effects.", map[string]any{"transfer_id": str("Original transfer ID")}, "transfer_id"),
		tool("file_transfer_cancel", "Request cancellation of the original transfer. Offline cancellation remains pending; already imported files are not undone.", map[string]any{"transfer_id": str("Original transfer ID")}, "transfer_id"),
	}
}
