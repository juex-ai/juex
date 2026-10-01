package managedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestFileToolFreezesAuthorizedLocations(t *testing.T) {
	environments := []execprotocol.Environment{
		{ID: "hosted", Kind: "hosted", AuthorizationVersion: 4, Capabilities: []execprotocol.Capability{execprotocol.Files}},
		{ID: "mac", Kind: "native", Online: false, AuthorizationVersion: 8, Capabilities: []execprotocol.Capability{execprotocol.Files}},
		{ID: "shell-only", Kind: "native", AuthorizationVersion: 3, Capabilities: []execprotocol.Capability{execprotocol.Shell}},
	}
	work := ToolWork{ID: "original-work", Scope: Scope{AgentID: "agent"}, Call: llm.Block{ToolName: "copy_file", Input: map[string]any{"source": map[string]any{"path": "source.bin"}, "target": map[string]any{"environment_id": "mac", "path": "destination.bin", "working_directory": "/Users/owner"}, "visibility": "fleet"}}}
	environment, request, err := prepareFileTransfer(work, environments)
	if err != nil {
		t.Fatal(err)
	}
	var spec FileTransferSpec
	if err := json.Unmarshal(request.Arguments, &spec); err != nil {
		t.Fatal(err)
	}
	if environment != "hosted" || request.ID != work.ID || spec.Source.EnvironmentID != "hosted" || spec.Source.AuthorizationVersion != 4 || spec.Target.EnvironmentID != "mac" || spec.Target.AuthorizationVersion != 8 || spec.Target.WorkingDirectory != "/Users/owner" || spec.Name != "source.bin" || spec.Visibility != "fleet" {
		t.Fatal("location or authority was not frozen", environment, request, spec)
	}
	for _, test := range []struct {
		name, tool string
		input      map[string]any
		err        error
	}{
		{"unknown device", "publish_file", map[string]any{"source": map[string]any{"environment_id": "other", "path": "file"}}, execprotocol.ErrDenied},
		{"missing files capability", "publish_file", map[string]any{"source": map[string]any{"environment_id": "shell-only", "path": "file"}}, execprotocol.ErrDenied},
		{"model forged version", "publish_file", map[string]any{"source": map[string]any{"path": "file", "authorization_version": 99}}, execprotocol.ErrInvalid},
		{"copy missing target", "copy_file", map[string]any{"source": map[string]any{"path": "file"}}, execprotocol.ErrInvalid},
		{"import cannot reshare", "import_file", map[string]any{"artifact_id": "artifact:original", "target": map[string]any{"path": "file"}, "visibility": "fleet"}, execprotocol.ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			work.Call.ToolName, work.Call.Input = test.tool, test.input
			if _, _, err := prepareFileTransfer(work, environments); !errors.Is(err, test.err) {
				t.Fatal(err)
			}
		})
	}
	work.Call = llm.Block{ToolName: "publish_file", Input: map[string]any{"source": map[string]any{"path": "file"}}}
	if _, _, err := prepareFileTransfer(work, environments[1:]); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("missing hosted environment silently selected a remote device", err)
	}
}

type cancellationToolGateway struct {
	ToolGateway
	err   error
	state execprotocol.State
}

func (g cancellationToolGateway) CancelPrepared(context.Context, Scope, string, string) (execprotocol.State, error) {
	return g.state, g.err
}

type cancellationFileGateway struct {
	FileGateway
	err   error
	state execprotocol.State
}

func (g cancellationFileGateway) CancelFileRequest(context.Context, Scope, string) (execprotocol.State, error) {
	return g.state, g.err
}

func TestCancellationRetainsResponsibilityUntilExecutionSettles(t *testing.T) {
	for _, kind := range []string{"write", "copy_file"} {
		for _, err := range []error{execprotocol.ErrNotFound, execprotocol.ErrDenied, execprotocol.ErrUnavailable, nil} {
			runner := toolRunner{gateway: cancellationToolGateway{err: err, state: execprotocol.Cancelled}, files: cancellationFileGateway{err: err, state: execprotocol.Cancelled}}
			work := ToolWork{ID: "prepared-work", EnvironmentID: "environment", Request: execprotocol.Request{ID: "prepared-work"}, Call: llm.Block{ToolName: kind}, Cancelled: true}
			outcome := runner.execute(context.Background(), &work)
			if err == nil {
				if outcome.State != "cancelled" || outcome.OperationLive {
					t.Fatal("accepted cancellation retained Runtime delivery", kind, outcome)
				}
			} else if !outcome.OperationLive || outcome.RetryAfter <= 0 || outcome.State != "waiting" {
				t.Fatal("unaccepted cancellation responsibility lost", kind, err, outcome)
			}
		}
		for _, state := range []execprotocol.State{execprotocol.Running, "dispatched", execprotocol.Unknown} {
			runner := toolRunner{gateway: cancellationToolGateway{state: state}, files: cancellationFileGateway{state: state}}
			work := ToolWork{ID: "original", EnvironmentID: "environment", Request: execprotocol.Request{ID: "original"}, Call: llm.Block{ToolName: kind}, Cancelled: true}
			outcome := runner.execute(context.Background(), &work)
			if state == execprotocol.Unknown {
				if outcome.State != "unknown" || !outcome.IsError {
					t.Fatal("unknown cancellation was called successful", outcome)
				}
			} else if outcome.State != "waiting" || !outcome.OperationLive || outcome.RetryAfter <= 0 {
				t.Fatal("cancel acknowledgment was confused with process termination", outcome)
			}
		}
	}
}
