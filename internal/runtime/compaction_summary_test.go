package runtime

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestBuildCompactionSummaryRequest_UsesPreviousSummaryAndTruncatesToolResult(t *testing.T) {
	prev := testMsg("compact-1", llm.RoleUser, "Summary of earlier conversation:\nGoal\nold")
	prev.Kind = llm.MessageKindCompact
	input := []llm.Message{
		{ID: "tool-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu1", Content: strings.Repeat("x", 50)}}},
	}
	sys, hist := buildCompactionSummaryRequest("base", prev, input, compactionPolicy{ToolResultMaxChars: 10})
	if !strings.Contains(sys, "Goal") || !strings.Contains(sys, "Tool Failures") {
		t.Fatalf("system prompt missing required headings: %s", sys)
	}
	body := hist[0].FirstText()
	if !strings.Contains(body, "<previous-summary>") || !strings.Contains(body, "truncated") {
		t.Fatalf("summary request body = %s", body)
	}
}

func TestBuildCompactionSummaryRequest_TruncatesTextAndToolUseInput(t *testing.T) {
	input := []llm.Message{
		{ID: "large", Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: strings.Repeat("t", 50)},
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "write", Input: map[string]any{"payload": strings.Repeat("x", 50)}},
		}},
	}
	_, hist := buildCompactionSummaryRequest("", llm.Message{}, input, compactionPolicy{ToolResultMaxChars: 10})
	body := hist[0].FirstText()
	if !strings.Contains(body, "text: tttttttttt ...(truncated, total 50 bytes)") {
		t.Fatalf("text was not truncated:\n%s", body)
	}
	if !strings.Contains(body, "tool_use tu1 write:") || !strings.Contains(body, "truncated") {
		t.Fatalf("tool use input was not truncated:\n%s", body)
	}
	if strings.Contains(body, strings.Repeat("x", 30)) {
		t.Fatalf("tool use input leaked untruncated payload:\n%s", body)
	}
}
