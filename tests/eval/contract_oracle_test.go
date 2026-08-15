package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestContractOracleValidatesAgentSmokeArtifacts(t *testing.T) {
	dir := t.TempDir()
	conversation := filepath.Join(dir, "conversation.jsonl")
	events := filepath.Join(dir, "events.jsonl")
	writeContractJSONL(t, conversation,
		llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "read_1", ToolName: "read"},
			{Type: llm.BlockToolUse, ToolUseID: "write_1", ToolName: "write"},
			{Type: llm.BlockToolUse, ToolUseID: "edit_1", ToolName: "edit"},
			{Type: llm.BlockToolUse, ToolUseID: "grep_1", ToolName: "grep"},
			{Type: llm.BlockToolUse, ToolUseID: "exec_1", ToolName: "exec_command", Input: map[string]any{"tty": true}},
			{Type: llm.BlockToolUse, ToolUseID: "stdin_1", ToolName: "write_stdin"},
		}},
		llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "exec_1", Content: "TTY-DONE contract-token\nProcess exited with code 0"},
		}},
	)
	writeContractJSONL(t, events,
		map[string]any{"type": "tool.completed", "payload": map[string]any{
			"name": "exec_command",
			"outcome": map[string]any{"block": map[string]any{
				"type": "tool_result", "content": "INSTALL 10%\r\nPROMPT approve install?",
			}},
			"result": map[string]any{
				"session_id": 4,
				"running":    true,
			},
		}},
		map[string]any{"type": "tool.completed", "payload": map[string]any{
			"name": "write_stdin",
			"outcome": map[string]any{"block": map[string]any{
				"type": "tool_result", "content": "TTY-DONE contract-token",
			}},
			"result": map[string]any{
				"running":   false,
				"exit_code": 0,
			},
		}},
	)

	report := ValidateContractArtifacts(ContractArtifacts{
		ConversationPath: conversation,
		EventsPath:       events,
	}, ContractExpectations{
		RequiredTools:   []string{"read", "write", "edit", "grep", "exec_command", "write_stdin"},
		RequireTTYExec:  true,
		ExecResultToken: "contract-token",
		Events: EventContractExpectations{
			RejectOutputDeltas:                true,
			RequireTerminalInstallProgress:    true,
			RequireTerminalInteractivePrompt:  true,
			DoneToken:                         "contract-token",
			RequireTerminalCarriageReturn:     true,
			RequireWriteStdinCompleted:        true,
			RequireStructuredExecRunning:      true,
			RequireStructuredWriteStdinResult: true,
		},
	})
	if !report.Passed {
		t.Fatalf("report failed: %+v", report)
	}
}

func TestContractOracleReportsMissingStructuredShellResult(t *testing.T) {
	dir := t.TempDir()
	conversation := filepath.Join(dir, "conversation.jsonl")
	events := filepath.Join(dir, "events.jsonl")
	writeContractJSONL(t, conversation,
		llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "stdin_1", ToolName: "write_stdin"},
		}},
	)
	writeContractJSONL(t, events,
		map[string]any{"type": "tool.completed", "payload": map[string]any{"name": "write_stdin"}},
	)

	report := ValidateContractArtifacts(ContractArtifacts{
		ConversationPath: conversation,
		EventsPath:       events,
	}, ContractExpectations{
		Events: EventContractExpectations{
			DoneToken:                         "contract-token",
			RequireStructuredWriteStdinResult: true,
		},
	})
	if report.Passed {
		t.Fatalf("report passed, want failure")
	}
	if !strings.Contains(report.Summary(), "structured write_stdin result") {
		t.Fatalf("summary = %q, want structured write_stdin result", report.Summary())
	}
}

func writeContractJSONL(t *testing.T, path string, rows ...any) {
	t.Helper()
	var b strings.Builder
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(encoded)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
