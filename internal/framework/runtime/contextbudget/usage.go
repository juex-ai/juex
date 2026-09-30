package contextbudget

import (
	"math"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/prompt"
)

const ContextUsageResponseKey = "response"

const (
	tokenCalibrationAlpha    = 0.3
	tokenCalibrationMinRatio = 0.5
	tokenCalibrationMaxRatio = 3.0
)

type TokenEstimateCalibration struct {
	ratio       float64
	initialized bool
}

func (c *TokenEstimateCalibration) Update(realTokens, estimatedTokens int) {
	if c == nil || realTokens <= 0 || estimatedTokens <= 0 {
		return
	}
	observed := clampTokenCalibrationRatio(float64(realTokens) / float64(estimatedTokens))
	if !c.initialized {
		c.ratio = observed
		c.initialized = true
		return
	}
	c.ratio = clampTokenCalibrationRatio(c.ratio*(1-tokenCalibrationAlpha) + observed*tokenCalibrationAlpha)
}

func (c TokenEstimateCalibration) Apply(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	if !c.initialized {
		return tokens
	}
	return int(math.Ceil(float64(tokens) * c.ratio))
}

func clampTokenCalibrationRatio(ratio float64) float64 {
	if ratio < tokenCalibrationMinRatio {
		return tokenCalibrationMinRatio
	}
	if ratio > tokenCalibrationMaxRatio {
		return tokenCalibrationMaxRatio
	}
	return ratio
}

func ContextUsageSnapshot(model string, contextWindow, defaultContextWindow int, usage llm.Usage, sections []prompt.Section, tools []llm.ToolSpec, history []llm.Message) llm.ContextUsage {
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}
	if model == "" {
		model = "unknown"
	}
	systemTools, mcpTools := SplitContextTools(tools)
	breakdown := []llm.ContextUsagePart{
		{Key: "system_prompt", Label: "System prompt", Tokens: EstimateSystemPromptTokens(sections)},
		{Key: "system_tools", Label: "System tools", Tokens: llm.EstimateToolTokens(systemTools)},
		{Key: "mcp_tools", Label: "MCP tools", Tokens: llm.EstimateToolTokens(mcpTools)},
		{Key: "skills", Label: "Skills", Tokens: EstimateSectionTokens(sections, "skills")},
		{Key: "compact_summary", Label: "Compact summary", Tokens: EstimateCompactSummaryTokens(history)},
		{Key: "context_artifacts", Label: "Context artifact references", Tokens: EstimateContextArtifactTokens(history)},
		{Key: "messages", Label: "Messages", Tokens: EstimateOrdinaryMessageTokens(history)},
		{Key: ContextUsageResponseKey, Label: "Response", Tokens: usage.OutputTokens},
	}
	if usage.InputTokens <= 0 {
		usage.InputTokens = EstimatedInputTokens(breakdown)
	}
	return llm.ContextUsage{
		Model:             model,
		ContextWindow:     contextWindow,
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		TotalTokens:       usage.TotalTokens(),
		Breakdown:         breakdown,
	}
}

func EstimatedInputTokens(parts []llm.ContextUsagePart) int {
	var total int
	for _, part := range parts {
		if part.Key == ContextUsageResponseKey {
			continue
		}
		total += part.Tokens
	}
	return total
}

func EstimateCompactSummaryTokens(history []llm.Message) int {
	var compact []llm.Message
	for _, msg := range history {
		if msg.Kind == llm.MessageKindCompact {
			compact = append(compact, msg)
		}
	}
	return llm.EstimateMessageTokens(compact)
}

func EstimateContextArtifactTokens(history []llm.Message) int {
	var tokens int
	for _, msg := range history {
		for _, block := range msg.Blocks {
			if block.Artifact == nil {
				continue
			}
			tokens += llm.EstimateTextTokens(block.Text) + llm.EstimateTextTokens(block.Content)
		}
	}
	return tokens
}

func EstimateOrdinaryMessageTokens(history []llm.Message) int {
	ordinary := make([]llm.Message, 0, len(history))
	for _, msg := range history {
		if msg.Kind == llm.MessageKindCompact {
			continue
		}
		cloned := msg
		cloned.Blocks = nil
		for _, block := range msg.Blocks {
			if block.Artifact != nil {
				continue
			}
			cloned.Blocks = append(cloned.Blocks, block)
		}
		ordinary = append(ordinary, cloned)
	}
	return llm.EstimateMessageTokens(ordinary)
}

func SplitContextTools(tools []llm.ToolSpec) ([]llm.ToolSpec, []llm.ToolSpec) {
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

func EstimateSystemPromptTokens(sections []prompt.Section) int {
	filtered := make([]prompt.Section, 0, len(sections))
	for _, section := range sections {
		switch section.Key {
		case "skills":
			continue
		default:
			filtered = append(filtered, section)
		}
	}
	return llm.EstimateTextTokens(prompt.JoinSections(filtered))
}

func EstimateSectionTokens(sections []prompt.Section, key string) int {
	var tokens int
	for _, section := range sections {
		if section.Key == key {
			tokens += llm.EstimateTextTokens(section.Text)
		}
	}
	return tokens
}
