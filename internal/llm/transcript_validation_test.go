package llm

import (
	"strings"
	"testing"
)

func TestValidateToolTranscriptAcceptsValidBatch(t *testing.T) {
	history := []Message{
		TextMessage(RoleUser, "batch"),
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockToolUse, ToolUseID: "a", ToolName: "read"},
			{Type: BlockToolUse, ToolUseID: "b", ToolName: "grep"},
		}},
		{Role: RoleUser, Blocks: []Block{
			{Type: BlockToolResult, ToolUseID: "a", Content: "file"},
			{Type: BlockToolResult, ToolUseID: "b", Content: "match"},
		}},
		TextMessage(RoleAssistant, "done"),
	}
	if err := ValidateToolTranscript(history); err != nil {
		t.Fatalf("ValidateToolTranscript: %v", err)
	}
}

func TestValidateToolTranscriptRejectsDanglingToolUse(t *testing.T) {
	history := []Message{
		TextMessage(RoleUser, "search"),
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "call_missing",
			ToolName:  "grep",
		}}},
		TextMessage(RoleUser, "new input"),
	}
	err := ValidateToolTranscript(history)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "call_missing") ||
		!strings.Contains(err.Error(), "missing tool_result") ||
		!strings.Contains(err.Error(), "before message 3 block 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildProviderContextRejectsCanonicalDanglingToolUse(t *testing.T) {
	history := []Message{
		TextMessage(RoleUser, "search"),
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "call_projected",
			ToolName:  "grep",
		}}},
	}
	_, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, ProviderContextOptions{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "call_projected") {
		t.Fatalf("error = %v", err)
	}
}
