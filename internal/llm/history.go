package llm

// compactHistoryForProvider drops blocks that cannot be sent to model
// providers and merges adjacent messages with the same role. That preserves
// provider role ordering even when older transcripts contain empty assistant
// messages.
func compactHistoryForProvider(history []Message) []Message {
	out := make([]Message, 0, len(history))
	for _, m := range history {
		blocks := compactBlocksForProvider(m.Blocks)
		if len(blocks) == 0 {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Role == m.Role {
			out[len(out)-1].Blocks = append(out[len(out)-1].Blocks, blocks...)
			continue
		}
		out = append(out, Message{Role: m.Role, Blocks: blocks})
	}
	return out
}

func compactBlocksForProvider(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
		case BlockReasoning:
			if b.Text == "" && b.Content == "" {
				continue
			}
		case BlockToolUse:
			if b.ToolUseID == "" || b.ToolName == "" {
				continue
			}
		case BlockToolResult:
			if b.ToolUseID == "" {
				continue
			}
		default:
			continue
		}
		out = append(out, b)
	}
	return out
}
