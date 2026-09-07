package llm

// compactHistoryForProvider drops blocks that cannot be sent to model
// providers and merges adjacent messages with the same role and app-level kind.
// Keeping kind boundaries lets provider adapters distinguish durable user input
// from transient runtime context while still coalescing ordinary chat history.
func compactHistoryForProvider(history []Message) []Message {
	out := make([]Message, 0, len(history))
	for _, m := range history {
		blocks := compactBlocksForProvider(m.Blocks)
		if len(blocks) == 0 {
			continue
		}
		if len(out) > 0 && providerMessagesMergeable(out[len(out)-1], m) {
			out[len(out)-1].Blocks = append(out[len(out)-1].Blocks, blocks...)
			continue
		}
		projected := m
		projected.Blocks = blocks
		out = append(out, projected)
	}
	return out
}

func providerMessagesMergeable(left, right Message) bool {
	return left.Role == right.Role && left.Kind == right.Kind
}

func compactBlocksForProvider(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case BlockText:
			if b.Text == "" {
				continue
			}
		case BlockImage:
			// Keep image blocks even when their file is currently unavailable;
			// adapters will degrade them to provider-visible reference text.
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
