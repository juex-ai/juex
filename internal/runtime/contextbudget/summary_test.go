package contextbudget

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/llm"
)

func TestBuildCompactionSummaryRequest_UsesPreviousSummaryAndTruncatesToolResult(t *testing.T) {
	prev := testMsg("compact-1", llm.RoleUser, "Summary of earlier conversation:\nGoal\nold")
	prev.Kind = llm.MessageKindCompact
	input := []llm.Message{
		{ID: "tool-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu1", Content: strings.Repeat("x", 50)}}},
	}
	sys, hist := BuildCompactionSummaryRequest("base", prev, input, SummaryState{}, Policy{ToolResultMaxChars: 10}, "")
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
	_, hist := BuildCompactionSummaryRequest("", llm.Message{}, input, SummaryState{}, Policy{ToolResultMaxChars: 10}, "")
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

	_, hist := BuildCompactionSummaryRequest("", llm.Message{}, input, SummaryState{}, Policy{ToolResultMaxChars: 2000}, "")
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

	sys, hist := BuildCompactionSummaryRequest("", llm.Message{}, input, SummaryState{}, Policy{ToolResultMaxChars: 400}, "")

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
	var input []llm.Message
	for i := 0; i < 80; i++ {
		msg := llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", 2000)))
		msg.ID = fmt.Sprintf("msg-%02d", i)
		input = append(input, msg)
	}
	policy := Policy{
		ToolResultMaxChars: 2000,
		TriggerTokens:      900,
		SummaryMaxTokens:   100,
	}

	sys, hist := BuildCompactionSummaryRequest("base", llm.Message{}, input, SummaryState{}, policy, "")

	limit := policy.TriggerTokens - policy.SummaryMaxTokens
	if got := EstimateContextTokens(sys, nil, hist); got > limit {
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

func TestBuildCompactionSummaryRequest_PreservesAuthoritativeStateWhenTranscriptIsOmitted(t *testing.T) {
	goal := SummaryGoal{
		Description:  "Ship compaction fidelity",
		Acceptance:   "Goal and Notes survive compaction:\n- [ ] preserve first line\n- [ ] preserve second line",
		Status:       "in_progress",
		StatusReason: "verification remains:\nrun the live evaluation",
	}
	state := SummaryState{
		Goal:  &goal,
		Notes: "- [x] map the runtime\n- [ ] run the live compaction evaluation",
	}
	var input []llm.Message
	for i := 0; i < 12; i++ {
		msg := llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", 500)))
		msg.ID = fmt.Sprintf("msg-%02d", i)
		input = append(input, msg)
	}
	policy := Policy{
		ToolResultMaxChars: 500,
		TriggerTokens:      800,
		SummaryMaxTokens:   100,
	}

	sys, hist := BuildCompactionSummaryRequest("base", llm.Message{}, input, state, policy, "")

	if !strings.Contains(sys, "Authoritative session state is provided below") {
		t.Fatalf("system prompt missing authoritative-state instruction:\n%s", sys)
	}
	if !strings.Contains(sys, "copy every unfinished - [ ] checklist item's text verbatim into Next Steps and do not omit one") {
		t.Fatalf("system prompt does not require exact unfinished Notes retention:\n%s", sys)
	}
	body := hist[0].FirstText()
	for _, want := range []string{
		"<authoritative-session-state>",
		state.Notes,
		"</authoritative-session-state>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary body missing authoritative state %q:\n%s", want, body)
		}
	}
	if got := summaryGoalFromBody(t, body); got != goal {
		t.Fatalf("summary goal = %+v, want lossless %+v", got, goal)
	}
	if !strings.Contains(body, "messages omitted") || strings.Contains(body, "message-00") {
		t.Fatalf("transcript was not omitted before authoritative state:\n%s", body)
	}
	limit := policy.TriggerTokens - policy.SummaryMaxTokens
	if got := EstimateContextTokens(sys, nil, hist); got > limit {
		t.Fatalf("summary request tokens = %d, want <= %d", got, limit)
	}
}

func summaryGoalFromBody(t *testing.T, body string) SummaryGoal {
	t.Helper()
	const openTag = "<goal-contract>\n"
	const closeTag = "\n</goal-contract>"
	start := strings.Index(body, openTag)
	if start < 0 {
		t.Fatalf("summary body missing %s:\n%s", strings.TrimSpace(openTag), body)
	}
	start += len(openTag)
	end := strings.Index(body[start:], closeTag)
	if end < 0 {
		t.Fatalf("summary body missing %s:\n%s", strings.TrimSpace(closeTag), body)
	}
	var goal SummaryGoal
	if err := json.Unmarshal([]byte(body[start:start+end]), &goal); err != nil {
		t.Fatalf("decode summary goal: %v\n%s", err, body)
	}
	return goal
}

func TestCompactionSummaryRequestTokenLimitCapsLargeWindows(t *testing.T) {
	policy := Policy{
		TriggerTokens:    239616,
		SummaryMaxTokens: 2048,
	}
	if got := CompactionSummaryRequestTokenLimit(policy); got != 16000 {
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
	policy := Policy{ToolResultMaxChars: 500}
	wantStart := len(input) - 3
	limit := EstimateContextTokens(sys, nil, []llm.Message{
		llm.TextMessage(llm.RoleUser, BuildCompactionSummaryBody(llm.Message{}, input[wantStart:], SummaryState{}, policy.ToolResultMaxChars, wantStart)),
	})
	if CompactionSummaryFits(sys, llm.Message{}, input[wantStart-1:], SummaryState{}, policy.ToolResultMaxChars, wantStart-1, limit) {
		t.Fatal("test setup invalid: four-message suffix should not fit")
	}

	selected, omitted, _ := FitCompactionSummaryInput(sys, llm.Message{}, input, SummaryState{}, policy, limit)

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

func TestFitCompactionSummaryInputFallbackRespectsSmallCharLimit(t *testing.T) {
	input := []llm.Message{llm.TextMessage(llm.RoleUser, strings.Repeat("x", 1000))}
	_, omitted, maxChars := FitCompactionSummaryInput("system", llm.Message{}, input, SummaryState{}, Policy{ToolResultMaxChars: 64}, 1)

	if omitted != 1 {
		t.Fatalf("omitted = %d, want 1", omitted)
	}
	if maxChars != 64 {
		t.Fatalf("fallback max chars = %d, want 64", maxChars)
	}
}

func TestTruncateForSummaryPreservesUTF8(t *testing.T) {
	got := truncateForSummary("界界界", 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string is invalid UTF-8: %q", got)
	}
	if got != "界" {
		t.Fatalf("truncated string = %q, want one full rune", got)
	}
}
