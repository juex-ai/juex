package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

func buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, policy compactionPolicy) (string, []llm.Message) {
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

Preserve exact file paths, commands, error strings, identifiers, decisions, and current next steps. If a previous summary is provided, update it: keep still-correct information, add new progress, remove stale information, and refresh next steps. Do not answer the latest user request. Do not call tools.`)

	var body strings.Builder
	if previous.FirstText() != "" {
		body.WriteString("<previous-summary>\n")
		body.WriteString(previous.FirstText())
		body.WriteString("\n</previous-summary>\n\n")
	}
	body.WriteString("<transcript-to-summarize>\n")
	for _, msg := range input {
		body.WriteString(serializeMessageForSummary(msg, policy.ToolResultMaxChars))
	}
	body.WriteString("</transcript-to-summarize>")
	return sys, []llm.Message{llm.TextMessage(llm.RoleUser, body.String())}
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
