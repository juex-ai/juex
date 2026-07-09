package contextbudget

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
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
		{Key: "system_tools", Label: "System tools", Tokens: EstimateToolTokens(systemTools)},
		{Key: "mcp_tools", Label: "MCP tools", Tokens: EstimateToolTokens(mcpTools)},
		{Key: "memory_files", Label: "Memory files", Tokens: EstimateSectionTokens(sections, "memory_files")},
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
	return EstimateMessageTokens(compact)
}

func EstimateContextArtifactTokens(history []llm.Message) int {
	var tokens int
	for _, msg := range history {
		for _, block := range msg.Blocks {
			if block.Artifact == nil {
				continue
			}
			tokens += EstimateTextTokens(block.Text) + EstimateTextTokens(block.Content)
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
	return EstimateMessageTokens(ordinary)
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
		case "memory_files", "skills":
			continue
		default:
			filtered = append(filtered, section)
		}
	}
	return EstimateTextTokens(prompt.JoinSections(filtered))
}

func EstimateSectionTokens(sections []prompt.Section, key string) int {
	var tokens int
	for _, section := range sections {
		if section.Key == key {
			tokens += EstimateTextTokens(section.Text)
		}
	}
	return tokens
}

func EstimateToolTokens(tools []llm.ToolSpec) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return EstimateCharsAsTokens(len(data))
}

func EstimateContextTokens(systemPrompt string, tools []llm.ToolSpec, history []llm.Message) int {
	return EstimateTextTokens(systemPrompt) +
		EstimateToolTokens(tools) +
		EstimateMessageTokens(history)
}

func EstimateMessageTokens(history []llm.Message) int {
	var tokens int
	for _, m := range history {
		tokens += EstimateCharsAsTokens(len(m.Role) + len(m.Kind) + 8)
		for _, b := range m.Blocks {
			tokens += EstimateCharsAsTokens(len(b.Type) + len(b.ToolUseID) + len(b.ToolName) + 8)
			tokens += EstimateTextTokens(b.Text)
			tokens += EstimateTextTokens(b.Content)
			if b.Media != nil {
				tokens += EstimateCharsAsTokens(EstimateMediaReferenceChars(b.Media))
			}
			if len(b.Input) > 0 {
				if data, err := json.Marshal(b.Input); err == nil {
					tokens += EstimateCharsAsTokens(len(data))
				}
			}
		}
	}
	return tokens
}

func EstimateMediaReferenceChars(media *llm.MediaRef) int {
	chars := len(media.ArtifactPath) + len(media.MediaType) + len(media.SHA256) + 24
	imageTokens := 85
	if media.Width > 0 && media.Height > 0 {
		pixels := int64(media.Width) * int64(media.Height)
		if pixels > 0 {
			estimated := int((pixels + 749) / 750)
			if estimated > imageTokens {
				imageTokens = estimated
			}
		}
	}
	return chars + imageTokens*4
}

func EstimateTextTokens(text string) int {
	var ascii, cjk, other int
	for _, r := range text {
		switch {
		case r <= 0x7f:
			ascii++
		case isCJKRune(r):
			cjk++
		default:
			other++
		}
	}
	return ceilDiv(ascii, 4) + cjk + ceilDiv(other, 3)
}

func EstimateCharsAsTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return ceilDiv(chars, 4)
}

func ceilDiv(n, d int) int {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

func isCJKRune(r rune) bool {
	return (r >= 0x3400 && r <= 0x4dbf) ||
		(r >= 0x4e00 && r <= 0x9fff) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0x20000 && r <= 0x2a6df) ||
		(r >= 0x2a700 && r <= 0x2b73f) ||
		(r >= 0x2b740 && r <= 0x2b81f) ||
		(r >= 0x2b820 && r <= 0x2ceaf) ||
		(r >= 0x3040 && r <= 0x309f) ||
		(r >= 0x30a0 && r <= 0x30ff) ||
		(r >= 0x31f0 && r <= 0x31ff) ||
		(r >= 0x1100 && r <= 0x11ff) ||
		(r >= 0x3130 && r <= 0x318f) ||
		(r >= 0xa960 && r <= 0xa97f) ||
		(r >= 0xac00 && r <= 0xd7af) ||
		(r >= 0xd7b0 && r <= 0xd7ff)
}
