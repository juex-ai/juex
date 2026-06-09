package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

// Keep recovery compaction well below the model context window: very large
// summary prompts can fit on paper but still time out before streaming.
const maxCompactionSummaryRequestTokens = 16000

func buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, policy compactionPolicy, instructions string) (string, []llm.Message) {
	sys := strings.TrimSpace(base + "\n\n" + `You are preparing a compact summary for continuing this conversation.

Return only a structured summary with these exact headings:

Goal
Constraints & Preferences
Progress
Key Decisions
Next Steps
Critical Context
Relevant Files
Tool Failures

Preserve exact file paths, commands, error strings, identifiers, decisions, and current next steps. In Critical Context, copy the actual values of labeled facts, task IDs, branch names, user constraints, safety guards, commands, and errors that a later turn may need. Never replace concrete facts with vague phrases such as "facts were stored", "facts were preserved", "noted", or "available in context"; include the values themselves. If a previous summary is provided, update it: keep still-correct information, add new progress, remove stale information, and refresh next steps. Do not answer the latest user request. Do not call tools.`)
	if focus := strings.TrimSpace(instructions); focus != "" {
		sys += "\n\nCompact Instructions:\n" + focus
	}

	omitted := 0
	maxChars := policy.ToolResultMaxChars
	if limit := compactionSummaryRequestTokenLimit(policy); limit > 0 {
		input, omitted, maxChars = fitCompactionSummaryInput(sys, previous, input, policy, limit)
	}
	body := buildCompactionSummaryBody(previous, input, maxChars, omitted)
	return sys, []llm.Message{llm.TextMessage(llm.RoleUser, body)}
}

func buildCompactionSummaryBody(previous llm.Message, input []llm.Message, maxChars, omitted int) string {
	var body strings.Builder
	if previous.FirstText() != "" {
		body.WriteString("<previous-summary>\n")
		body.WriteString(previous.FirstText())
		body.WriteString("\n</previous-summary>\n\n")
	}
	if omitted > 0 {
		fmt.Fprintf(&body, "<omitted-transcript>\n%d earlier messages omitted from this compaction request to fit the provider context window.\n</omitted-transcript>\n\n", omitted)
	}
	body.WriteString("<transcript-to-summarize>\n")
	for _, msg := range input {
		body.WriteString(serializeMessageForSummary(msg, maxChars))
	}
	body.WriteString("</transcript-to-summarize>")
	return body.String()
}

func compactionSummaryRequestTokenLimit(policy compactionPolicy) int {
	if policy.TriggerTokens <= 0 {
		return 0
	}
	limit := policy.TriggerTokens
	if policy.SummaryMaxTokens > 0 && policy.SummaryMaxTokens < limit {
		limit -= policy.SummaryMaxTokens
	}
	if limit > maxCompactionSummaryRequestTokens {
		limit = maxCompactionSummaryRequestTokens
	}
	if limit < 1 {
		return 1
	}
	return limit
}

func fitCompactionSummaryInput(sys string, previous llm.Message, input []llm.Message, policy compactionPolicy, limit int) ([]llm.Message, int, int) {
	maxChars := policy.ToolResultMaxChars
	if maxChars <= 0 {
		maxChars = 2000
	}
	for _, capChars := range compactionSummaryCaps(maxChars) {
		if compactionSummaryFits(sys, previous, input, capChars, 0, limit) {
			return input, 0, capChars
		}
		bestStart := -1
		for low, high := 0, len(input)-1; low <= high; {
			mid := low + (high-low)/2
			if compactionSummaryFits(sys, previous, input[mid:], capChars, mid, limit) {
				bestStart = mid
				high = mid - 1
			} else {
				low = mid + 1
			}
		}
		if bestStart >= 0 {
			out := append([]llm.Message(nil), input[bestStart:]...)
			return out, bestStart, capChars
		}
	}
	return nil, len(input), 256
}

func compactionSummaryCaps(maxChars int) []int {
	minCap := 256
	if maxChars < minCap {
		minCap = maxChars
	}
	caps := []int{maxChars}
	for n := maxChars / 2; n >= minCap; n /= 2 {
		if n != caps[len(caps)-1] {
			caps = append(caps, n)
		}
	}
	if caps[len(caps)-1] != minCap {
		caps = append(caps, minCap)
	}
	return caps
}

func compactionSummaryFits(sys string, previous llm.Message, input []llm.Message, maxChars, omitted, limit int) bool {
	body := buildCompactionSummaryBody(previous, input, maxChars, omitted)
	hist := []llm.Message{llm.TextMessage(llm.RoleUser, body)}
	return estimateContextTokens(sys, nil, hist) <= limit
}

func serializeMessageForSummary(msg llm.Message, toolResultMaxChars int) string {
	var sb strings.Builder
	id := msg.ID
	if id == "" {
		id = "unknown"
	}
	fmt.Fprintf(&sb, "\n[%s %s]\n", msg.Role, id)
	if msg.Kind != "" {
		fmt.Fprintf(&sb, "kind: %s\n", msg.Kind)
	}
	for _, block := range msg.Blocks {
		switch block.Type {
		case llm.BlockText:
			writeSummaryField(&sb, "text", block.Text, toolResultMaxChars)
		case llm.BlockReasoning:
			text := block.Text
			if text == "" {
				text = block.Content
			}
			writeSummaryField(&sb, "reasoning", text, toolResultMaxChars)
		case llm.BlockToolUse:
			input := "{}"
			if len(block.Input) > 0 {
				if data, err := json.Marshal(block.Input); err == nil {
					input = string(data)
				}
			}
			truncated := truncateForSummary(input, toolResultMaxChars)
			if truncated != input {
				fmt.Fprintf(&sb, "tool_use %s %s: %s ...(truncated, total %d bytes)\n", block.ToolUseID, block.ToolName, truncated, len(input))
			} else {
				fmt.Fprintf(&sb, "tool_use %s %s: %s\n", block.ToolUseID, block.ToolName, input)
			}
		case llm.BlockToolResult:
			content := block.Content
			truncated := truncateForSummary(content, toolResultMaxChars)
			if truncated != content {
				fmt.Fprintf(&sb, "tool_result %s error=%t: %s ...(truncated, total %d bytes)\n", block.ToolUseID, block.IsError, truncated, len(content))
			} else {
				fmt.Fprintf(&sb, "tool_result %s error=%t: %s\n", block.ToolUseID, block.IsError, content)
			}
		}
	}
	return sb.String()
}

func writeSummaryField(sb *strings.Builder, label, value string, maxChars int) {
	truncated := truncateForSummary(value, maxChars)
	if truncated != value {
		fmt.Fprintf(sb, "%s: %s ...(truncated, total %d bytes)\n", label, truncated, len(value))
		return
	}
	fmt.Fprintf(sb, "%s: %s\n", label, value)
}

func truncateForSummary(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}
