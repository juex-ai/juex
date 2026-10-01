package llm

import "encoding/json"

func EstimateToolTokens(tools []ToolSpec) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return EstimateCharsAsTokens(len(data))
}

func EstimateContextTokens(systemPrompt string, tools []ToolSpec, history []Message) int {
	return EstimateTextTokens(systemPrompt) +
		EstimateToolTokens(tools) +
		EstimateMessageTokens(history)
}

func EstimateMessageTokens(history []Message) int {
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

func EstimateMediaReferenceChars(media *MediaRef) int {
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
