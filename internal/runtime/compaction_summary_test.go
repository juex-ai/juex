package runtime

import (
	"fmt"
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
	sys, hist := buildCompactionSummaryRequest("base", prev, input, compactionPolicy{ToolResultMaxChars: 10}, "")
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
	_, hist := buildCompactionSummaryRequest("", llm.Message{}, input, compactionPolicy{ToolResultMaxChars: 10}, "")
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

func TestBuildCompactionSummaryRequest_BoundsOversizedTranscript(t *testing.T) {
	var input []llm.Message
	for i := 0; i < 80; i++ {
		msg := llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", 2000)))
		msg.ID = fmt.Sprintf("msg-%02d", i)
		input = append(input, msg)
	}
	policy := compactionPolicy{
		ToolResultMaxChars: 2000,
		TriggerTokens:      900,
		SummaryMaxTokens:   100,
	}

	sys, hist := buildCompactionSummaryRequest("base", llm.Message{}, input, policy, "")

	limit := policy.TriggerTokens - policy.SummaryMaxTokens
	if got := estimateContextTokens(sys, nil, hist); got > limit {
		t.Fatalf("summary request tokens = %d, want <= %d", got, limit)
	}
	body := hist[0].FirstText()
	if !strings.Contains(body, "messages omitted") {
		t.Fatalf("summary request did not record omitted transcript:\n%s", body)
	}
	if strings.Contains(body, "message-00") {
		t.Fatalf("oldest transcript should be omitted when over budget:\n%s", body)
	}
	if !strings.Contains(body, "message-79") {
		t.Fatalf("newest transcript should be retained when over budget:\n%s", body)
	}
}

func TestCompactionSummaryRequestTokenLimitCapsLargeWindows(t *testing.T) {
	policy := compactionPolicy{
		TriggerTokens:    239616,
		SummaryMaxTokens: 2048,
	}
	if got := compactionSummaryRequestTokenLimit(policy); got != 16000 {
		t.Fatalf("limit = %d, want 16000", got)
	}
}

func TestFitCompactionSummaryInputKeepsLongestFittingSuffix(t *testing.T) {
	var input []llm.Message
	for i := 0; i < 8; i++ {
		msg := llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", 400)))
		msg.ID = fmt.Sprintf("msg-%02d", i)
		input = append(input, msg)
	}
	sys := "summary system"
	policy := compactionPolicy{ToolResultMaxChars: 500}
	wantStart := len(input) - 3
	limit := estimateContextTokens(sys, nil, []llm.Message{
		llm.TextMessage(llm.RoleUser, buildCompactionSummaryBody(llm.Message{}, input[wantStart:], policy.ToolResultMaxChars, wantStart)),
	})
	if compactionSummaryFits(sys, llm.Message{}, input[wantStart-1:], policy.ToolResultMaxChars, wantStart-1, limit) {
		t.Fatal("test setup invalid: four-message suffix should not fit")
	}

	selected, omitted, _ := fitCompactionSummaryInput(sys, llm.Message{}, input, policy, limit)

	if omitted != wantStart {
		t.Fatalf("omitted = %d, want %d", omitted, wantStart)
	}
	if len(selected) != 3 {
		t.Fatalf("selected len = %d, want 3", len(selected))
	}
	if selected[0].ID != "msg-05" || selected[2].ID != "msg-07" {
		t.Fatalf("selected suffix = %+v", selected)
	}
}
