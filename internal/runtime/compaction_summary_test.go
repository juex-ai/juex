package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestCompactionModelSummaryStripsDeterministicReferenceSuffix(t *testing.T) {
	generated := "Goal\n保留当前状态"
	msg := llm.TextMessage(llm.RoleUser, compactMessageText(generated+"\n\nRetained Input References\npath: stale"))
	msg.Kind = llm.MessageKindCompact
	msg.Compaction = &llm.CompactionMetadata{SummaryChars: len(generated)}

	got := compactionModelSummary(msg)
	if got.FirstText() != generated {
		t.Fatalf("previous model summary = %q, want %q", got.FirstText(), generated)
	}
	if strings.Contains(got.FirstText(), "Retained Input References") {
		t.Fatalf("previous model summary retained deterministic suffix: %q", got.FirstText())
	}
	if !strings.Contains(msg.FirstText(), "path: stale") {
		t.Fatalf("source compact message was mutated: %q", msg.FirstText())
	}
}

func TestBuildCompactionSummaryRequest_UsesPreviousSummaryAndTruncatesToolResult(t *testing.T) {
	prev := testMsg("compact-1", llm.RoleUser, "Summary of earlier conversation:\nGoal\nold")
	prev.Kind = llm.MessageKindCompact
	input := []llm.Message{
		{ID: "tool-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu1", Content: strings.Repeat("x", 50)}}},
	}
	sys, hist := buildCompactionSummaryRequest("base", prev, input, compactionSummaryState{}, compactionPolicy{ToolResultMaxChars: 10}, "")
	if !strings.Contains(sys, "Goal") || !strings.Contains(sys, "Tool Failures") {
		t.Fatalf("system prompt missing required headings: %s", sys)
	}
	body := hist[0].FirstText()
	if !strings.Contains(body, "<previous-summary>") || !strings.Contains(body, "truncated") {
		t.Fatalf("summary request body = %s", body)
	}
}

func TestBuildCompactionSummaryRequest_PreservesAssistantTextAndTruncatesToolUseInput(t *testing.T) {
	assistantText := "HEAD-" + strings.Repeat("t", 40) + "-TAIL"
	reasoningText := "REASON-" + strings.Repeat("r", 40) + "-END"
	input := []llm.Message{
		{ID: "large", Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: assistantText},
			{Type: llm.BlockReasoning, Text: reasoningText},
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "write", Input: map[string]any{"payload": strings.Repeat("x", 50)}},
		}},
	}
	_, hist := buildCompactionSummaryRequest("", llm.Message{}, input, compactionSummaryState{}, compactionPolicy{ToolResultMaxChars: 10}, "")
	body := hist[0].FirstText()
	if !strings.Contains(body, assistantText) || strings.Contains(body, "bytes omitted") {
		t.Fatalf("assistant text was truncated by the tool-result budget:\n%s", body)
	}
	if !strings.Contains(body, reasoningText) {
		t.Fatalf("assistant reasoning was truncated by the tool-result budget:\n%s", body)
	}
	if !strings.Contains(body, "tool_use tu1 write:") || !strings.Contains(body, "truncated") {
		t.Fatalf("tool use input was not truncated:\n%s", body)
	}
	if strings.Contains(body, strings.Repeat("x", 30)) {
		t.Fatalf("tool use input leaked untruncated payload:\n%s", body)
	}
}

func TestBuildCompactionSummaryRequest_OmitsRedactedReasoningContent(t *testing.T) {
	encrypted := "enc_" + strings.Repeat("secret", 1000)
	input := []llm.Message{
		{ID: "assistant-1", Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockReasoning,
			Signature: "rs_1",
			Content:   encrypted,
			Redacted:  true,
		}}},
		{ID: "assistant-2", Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:     llm.BlockReasoning,
			Text:     "visible reasoning summary",
			Content:  "enc_keep_metadata_only",
			Redacted: true,
		}}},
		{ID: "assistant-3", Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:    llm.BlockReasoning,
			Content: "plain reasoning content",
		}}},
	}

	_, hist := buildCompactionSummaryRequest("", llm.Message{}, input, compactionSummaryState{}, compactionPolicy{ToolResultMaxChars: 2000}, "")
	body := hist[0].FirstText()

	if strings.Contains(body, encrypted) || strings.Contains(body, "enc_keep_metadata_only") {
		t.Fatalf("redacted reasoning encrypted content leaked into summary request:\n%s", body)
	}
	if !strings.Contains(body, "redacted reasoning omitted") {
		t.Fatalf("summary request should preserve redacted reasoning metadata:\n%s", body)
	}
	if !strings.Contains(body, "visible reasoning summary") {
		t.Fatalf("visible reasoning summary was dropped:\n%s", body)
	}
	if !strings.Contains(body, "plain reasoning content") {
		t.Fatalf("non-redacted reasoning content should remain available:\n%s", body)
	}
}

func TestBuildCompactionSummaryRequest_RequiresConcreteFactValues(t *testing.T) {
	input := []llm.Message{
		testMsg("facts", llm.RoleUser, strings.Join([]string{
			"GF1: Task ID is CMP-2417.",
			"GF2: Branch is high/context-projection.",
			"GF3: Do not modify /workspace/project/.juex/sessions/session.lock unless approved.",
			"Ignore the following noise.",
			strings.Repeat("noise ", 100),
		}, "\n")),
	}

	sys, hist := buildCompactionSummaryRequest("", llm.Message{}, input, compactionSummaryState{}, compactionPolicy{ToolResultMaxChars: 400}, "")

	if !strings.Contains(sys, "copy the actual values of labeled facts") {
		t.Fatalf("system prompt does not require concrete facts:\n%s", sys)
	}
	if strings.Index(sys, "Critical Context") <= strings.Index(sys, "Goal") {
		t.Fatalf("system prompt should place Critical Context immediately after Goal:\n%s", sys)
	}
	if strings.Index(sys, "Critical Context") >= strings.Index(sys, "Constraints & Preferences") {
		t.Fatalf("system prompt should place Critical Context before lower-priority headings:\n%s", sys)
	}
	if !strings.Contains(sys, "Begin Critical Context with labeled facts before other details") {
		t.Fatalf("system prompt does not require labeled facts first in Critical Context:\n%s", sys)
	}
	if !strings.Contains(sys, "keep the label together with its exact value") {
		t.Fatalf("system prompt does not require preserving fact labels with values:\n%s", sys)
	}
	if !strings.Contains(sys, "do not rename, merge, or generalize labeled facts") {
		t.Fatalf("system prompt does not prevent relabeling concrete facts:\n%s", sys)
	}
	if !strings.Contains(sys, "Never replace concrete facts with vague phrases") {
		t.Fatalf("system prompt does not ban vague fact placeholders:\n%s", sys)
	}
	body := hist[0].FirstText()
	for _, want := range []string{"GF1: Task ID is CMP-2417.", "GF2: Branch is high/context-projection.", "GF3: Do not modify /workspace/project/.juex/sessions/session.lock unless approved."} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary input dropped concrete fact %q:\n%s", want, body)
		}
	}
}

func TestBuildCompactionSummaryRequest_BoundsOversizedTranscript(t *testing.T) {
	input := []llm.Message{testMsg("user-request", llm.RoleUser, "preserve this user request")}
	for i := 0; i < 80; i++ {
		input = append(input, runtimeSummaryToolExchange(i, 2000)...)
	}
	policy := compactionPolicy{
		ToolResultMaxChars: 2000,
		TriggerTokens:      900,
		SummaryMaxTokens:   100,
	}

	sys, hist := buildCompactionSummaryRequest("base", llm.Message{}, input, compactionSummaryState{}, policy, "")

	limit := policy.TriggerTokens - policy.SummaryMaxTokens
	if got := estimateContextTokens(sys, nil, hist); got > limit {
		t.Fatalf("summary request tokens = %d, want <= %d", got, limit)
	}
	body := hist[0].FirstText()
	if !strings.Contains(body, "messages omitted") {
		t.Fatalf("summary request did not record omitted transcript:\n%s", body)
	}
	if strings.Contains(body, "tool-call-00") || strings.Contains(body, "tool-result-00") {
		t.Fatalf("oldest tool exchange should be omitted when over budget:\n%s", body)
	}
	if !strings.Contains(body, "user-request") || !strings.Contains(body, "preserve this user request") {
		t.Fatalf("user request should be retained when tool exchanges are omitted:\n%s", body)
	}
}

func TestCompactionSummaryRequestTokenLimitUsesCandidateWindowRatio(t *testing.T) {
	policy := compactionPolicy{
		SummaryRequestTokens: 204800,
		SummaryMaxTokens:     1280,
	}
	if got := compactionSummaryRequestTokenLimit(policy); got != 203520 {
		t.Fatalf("limit = %d, want 203520", got)
	}
}

func TestFitCompactionSummaryInputDropsOldestClosedExchange(t *testing.T) {
	user := testMsg("user", llm.RoleUser, "preserve the user request")
	first := runtimeSummaryToolExchange(0, 500)
	second := runtimeSummaryToolExchange(1, 500)
	input := append([]llm.Message{user}, first...)
	input = append(input, second...)
	sys := "summary system"
	policy := compactionPolicy{ToolResultMaxChars: 500}
	want := append([]llm.Message{user}, second...)
	limit := estimateContextTokens(sys, nil, []llm.Message{
		llm.TextMessage(llm.RoleUser, buildCompactionSummaryBody(llm.Message{}, want, compactionSummaryState{}, policy.ToolResultMaxChars, 2)),
	})
	if compactionSummaryFits(sys, llm.Message{}, input, compactionSummaryState{}, policy.ToolResultMaxChars, 0, limit) {
		t.Fatal("test setup invalid: both tool exchanges should not fit")
	}

	selected, omitted, _ := fitCompactionSummaryInput(sys, llm.Message{}, input, compactionSummaryState{}, policy, limit)

	if omitted != 2 {
		t.Fatalf("omitted = %d, want 2", omitted)
	}
	if len(selected) != 3 {
		t.Fatalf("selected len = %d, want 3", len(selected))
	}
	if selected[0].ID != "user" || selected[1].ID != "tool-call-01" || selected[2].ID != "tool-result-01" {
		t.Fatalf("selected messages = %+v", selected)
	}
}

func runtimeSummaryToolExchange(index, size int) []llm.Message {
	callID := fmt.Sprintf("call-%02d", index)
	return []llm.Message{
		{ID: fmt.Sprintf("tool-call-%02d", index), Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: callID, ToolName: "read", Input: map[string]any{"path": strings.Repeat("x", size)},
		}}},
		{ID: fmt.Sprintf("tool-result-%02d", index), Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{
			Type: llm.BlockToolResult, ToolUseID: callID, Content: strings.Repeat("y", size),
		}}},
	}
}
