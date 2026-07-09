package runtime

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
)

const contextUsageResponseKey = "response"

const (
	tokenCalibrationAlpha    = 0.3
	tokenCalibrationMinRatio = 0.5
	tokenCalibrationMaxRatio = 3.0
)

type tokenEstimateCalibration struct {
	ratio       float64
	initialized bool
}

func (c *tokenEstimateCalibration) update(realTokens, estimatedTokens int) {
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

func (c tokenEstimateCalibration) apply(tokens int) int {
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
		{Key: "compact_summary", Label: "Compact summary", Tokens: estimateCompactSummaryTokens(history)},
		{Key: "context_artifacts", Label: "Context artifact references", Tokens: estimateContextArtifactTokens(history)},
		{Key: "messages", Label: "Messages", Tokens: estimateOrdinaryMessageTokens(history)},
		{Key: contextUsageResponseKey, Label: "Response", Tokens: usage.OutputTokens},
	}
	if usage.InputTokens <= 0 {
		usage.InputTokens = estimatedInputTokens(breakdown)
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

func (e *Engine) contextUsageSnapshot(model string, contextWindow int, usage llm.Usage, sections []prompt.Section, tools []llm.ToolSpec, history []llm.Message) llm.ContextUsage {
	snapshot := contextUsageSnapshot(model, contextWindow, usage, sections, tools, history)
	for i := range snapshot.Breakdown {
		if snapshot.Breakdown[i].Key == contextUsageResponseKey {
			continue
		}
		snapshot.Breakdown[i].Tokens = e.applyTokenEstimateCalibration(snapshot.Breakdown[i].Tokens)
	}
	if usage.InputTokens <= 0 {
		snapshot.InputTokens = estimatedInputTokens(snapshot.Breakdown)
	}
	snapshot.TotalTokens = snapshot.InputTokens + snapshot.OutputTokens
	return snapshot
}

func estimatedInputTokens(parts []llm.ContextUsagePart) int {
	var total int
	for _, part := range parts {
		if part.Key == contextUsageResponseKey {
			continue
		}
		total += part.Tokens
	}
	return total
}

func estimateCompactSummaryTokens(history []llm.Message) int {
	var compact []llm.Message
	for _, msg := range history {
		if msg.Kind == llm.MessageKindCompact {
			compact = append(compact, msg)
		}
	}
	return estimateMessageTokens(compact)
}

func estimateContextArtifactTokens(history []llm.Message) int {
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

func estimateOrdinaryMessageTokens(history []llm.Message) int {
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
	return estimateMessageTokens(ordinary)
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
	return EstimateTextTokens(prompt.JoinSections(filtered))
}

func estimateSectionTokens(sections []prompt.Section, key string) int {
	var tokens int
	for _, section := range sections {
		if section.Key == key {
			tokens += EstimateTextTokens(section.Text)
		}
	}
	return tokens
}

func estimateToolTokens(tools []llm.ToolSpec) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return EstimateCharsAsTokens(len(data))
}

func estimateMessageTokens(history []llm.Message) int {
	var tokens int
	for _, m := range history {
		tokens += EstimateCharsAsTokens(len(m.Role) + len(m.Kind) + 8)
		for _, b := range m.Blocks {
			tokens += EstimateCharsAsTokens(len(b.Type) + len(b.ToolUseID) + len(b.ToolName) + 8)
			tokens += EstimateTextTokens(b.Text)
			tokens += EstimateTextTokens(b.Content)
			if b.Media != nil {
				tokens += EstimateCharsAsTokens(estimateMediaReferenceChars(b.Media))
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

func estimateMediaReferenceChars(media *llm.MediaRef) int {
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

func (e *Engine) updateTokenEstimateCalibration(realTokens, estimatedTokens int) {
	if e == nil {
		return
	}
	e.tokenCalibrationMu.Lock()
	defer e.tokenCalibrationMu.Unlock()
	e.tokenCalibration.update(realTokens, estimatedTokens)
}

func (e *Engine) applyTokenEstimateCalibration(tokens int) int {
	if e == nil {
		return tokens
	}
	e.tokenCalibrationMu.RLock()
	defer e.tokenCalibrationMu.RUnlock()
	return e.tokenCalibration.apply(tokens)
}

func (e *Engine) estimateContextTokens(systemPrompt string, tools []llm.ToolSpec, history []llm.Message) int {
	return e.applyTokenEstimateCalibration(estimateContextTokens(systemPrompt, tools, history))
}
