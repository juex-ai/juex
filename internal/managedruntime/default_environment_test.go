package managedruntime

import (
	"encoding/json"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func defaultEnvironmentFixture(t *testing.T) []execprotocol.Environment {
	t.Helper()
	var environments []execprotocol.Environment
	if err := json.Unmarshal([]byte(`[
		{"id":"hosted","kind":"hosted","capabilities":["files","shell","mcp"],"working_directory":"/workspace","authorization_version":2},
		{"id":"mac","kind":"native","default":true,"capabilities":["files","shell","mcp"],"working_directory":"/Users/owner/agent-a","authorization_version":7}
	]`), &environments); err != nil {
		t.Fatal(err)
	}
	return environments
}

func TestDefaultEnvironmentFreezesLocationForNewOperations(t *testing.T) {
	for _, kind := range []string{"read", "write", "edit", "glob", "grep", "exec_command", "mcp_connect"} {
		t.Run(kind, func(t *testing.T) {
			work := ToolWork{ID: "request", Scope: Scope{AgentID: "agent"}, Call: llm.Block{ToolName: kind, Input: map[string]any{}}}
			environment, request, err := prepareExecution(work, defaultEnvironmentFixture(t))
			var args map[string]any
			_ = json.Unmarshal(request.Arguments, &args)
			if err != nil || environment != "mac" || request.AuthorizationVersion != 7 || args["working_directory"] != "/Users/owner/agent-a" {
				t.Fatalf("environment=%q request=%+v args=%v err=%v", environment, request, args, err)
			}
			if len(work.Call.Input) != 0 {
				t.Fatal("preparation changed the original model call")
			}
		})
	}
}

func TestDefaultEnvironmentDoesNotOverrideExplicitLocationOrHandle(t *testing.T) {
	for _, input := range []map[string]any{
		{"environment_id": "hosted", "working_directory": "/home/agent"},
		{"handle": map[string]any{"environment_id": "hosted", "operation_id": "original"}},
	} {
		kind := "exec_command"
		if input["handle"] != nil {
			kind = "write_stdin"
		}
		environment, request, err := prepareExecution(ToolWork{Call: llm.Block{ToolName: kind, Input: input}}, defaultEnvironmentFixture(t))
		var args map[string]any
		_ = json.Unmarshal(request.Arguments, &args)
		if err != nil || environment != "hosted" {
			t.Fatalf("environment=%q err=%v", environment, err)
		}
		if kind == "write_stdin" && (args["operation_id"] != "original" || args["working_directory"] != nil) {
			t.Fatalf("handle was reinterpreted: %v", args)
		}
		if kind == "exec_command" && args["working_directory"] != "/home/agent" {
			t.Fatalf("explicit directory replaced: %v", args)
		}
	}
}

func TestDefaultEnvironmentUnavailableDoesNotChooseHosted(t *testing.T) {
	environments := defaultEnvironmentFixture(t)[:1]
	_, _, err := prepareExecution(ToolWork{Call: llm.Block{ToolName: "exec_command", Input: map[string]any{}}}, environments)
	if err == nil {
		t.Fatal("unavailable default silently redirected to hosted")
	}
}

func TestDefaultEnvironmentFileTransferFreezesBothDirectories(t *testing.T) {
	environment, request, err := prepareFileTransfer(ToolWork{ID: "transfer", Call: llm.Block{ToolName: "copy_file", Input: map[string]any{
		"source": map[string]any{"path": "in.txt"}, "target": map[string]any{"environment_id": "hosted", "path": "out.txt", "working_directory": "/workspace"},
	}}}, defaultEnvironmentFixture(t))
	var spec FileTransferSpec
	_ = json.Unmarshal(request.Arguments, &spec)
	if err != nil || environment != "mac" || spec.Source == nil || spec.Source.WorkingDirectory != "/Users/owner/agent-a" || spec.Target == nil || spec.Target.WorkingDirectory != "/workspace" {
		t.Fatalf("environment=%q spec=%+v err=%v", environment, spec, err)
	}
}
