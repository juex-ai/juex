package runtime

import (
	"encoding/json"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
)

func contextUsageSnapshot(model string, contextWindow int, usage llm.Usage, sections []prompt.Section, tools []llm.ToolSpec, history []llm.Message) llm.ContextUsage {
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	if model == "" {
		model = "unknown"
	}
	systemTools, mcpTools := splitContextTools(tools)
	breakdown := []llm.ContextUsagePart{
		{Key: "system_prompt", Label: "System prompt", Tokens: estimateSystemPromptTokens(sections)},
		{Key: "system_tools", Label: "System tools", Tokens: estimateToolTokens(systemTools)},
		{Key: "mcp_tools", Label: "MCP tools", Tokens: estimateToolTokens(mcpTools)},
		{Key: "memory_files", Label: "Memory files", Tokens: estimateSectionTokens(sections, "memory_files")},
		{Key: "skills", Label: "Skills", Tokens: estimateSectionTokens(sections, "skills")},
		{Key: "messages", Label: "Messages", Tokens: estimateMessageTokens(history)},
		{Key: "response", Label: "Response", Tokens: usage.OutputTokens},
	}
	return llm.ContextUsage{
		Model:         model,
		ContextWindow: contextWindow,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens(),
		Breakdown:     breakdown,
	}
}

func splitContextTools(tools []llm.ToolSpec) ([]llm.ToolSpec, []llm.ToolSpec) {
	systemTools := make([]llm.ToolSpec, 0, len(tools))
	mcpTools := make([]llm.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "mcp__") {
			mcpTools = append(mcpTools, tool)
			continue
		}
		systemTools = append(systemTools, tool)
	}
	return systemTools, mcpTools
}

func estimateSystemPromptTokens(sections []prompt.Section) int {
	filtered := make([]prompt.Section, 0, len(sections))
	for _, section := range sections {
		switch section.Key {
		case "memory_files", "skills":
			continue
		default:
			filtered = append(filtered, section)
		}
	}
	return estimateCharsAsTokens(len(prompt.JoinSections(filtered)))
}

func estimateSectionTokens(sections []prompt.Section, key string) int {
	var chars int
	for _, section := range sections {
		if section.Key == key {
			chars += len(section.Text)
		}
	}
	return estimateCharsAsTokens(chars)
}

func estimateToolTokens(tools []llm.ToolSpec) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return estimateCharsAsTokens(len(data))
}

func estimateMessageTokens(history []llm.Message) int {
	var chars int
	for _, m := range history {
		chars += len(m.Role) + len(m.Kind) + 8
		for _, b := range m.Blocks {
			chars += len(b.Type) + len(b.Text) + len(b.Content) + len(b.ToolUseID) + len(b.ToolName) + 8
			if len(b.Input) > 0 {
				if data, err := json.Marshal(b.Input); err == nil {
					chars += len(data)
				}
			}
		}
	}
	return estimateCharsAsTokens(chars)
}

func estimateCharsAsTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}
