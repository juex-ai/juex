package tools

import "github.com/juex-ai/juex/internal/sandbox"

const chunkedWriteGuidePointer = "MUST load the `juex-chunked-write` skill before first use."

type ChunkedWriteToolProvider struct{}

func (ChunkedWriteToolProvider) definitions(BuiltinDefinitionOptions) []ToolDefinition {
	return []ToolDefinition{
		writeBeginToolDefinition(),
		writeChunkToolDefinition(),
		writeCommitToolDefinition(),
		writeAbortToolDefinition(),
	}
}

func (ChunkedWriteToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	manager := newChunkWriteManager(ctx.WorkDir, sandbox.NewPathGuard(ctx.WorkDir, ctx.Sandbox))
	if ctx.ChunkedWrites != nil {
		manager = ctx.ChunkedWrites
	}
	return []Tool{
		writeBeginTool(manager),
		writeChunkTool(manager),
		writeCommitTool(manager),
		writeAbortTool(manager),
	}
}

func writeBeginToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_begin",
		Group:       ToolGroupChunkedWrite,
		Description: "Begin a long-file write; use write for short content. " + chunkedWriteGuidePointer,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"mode": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
}

func writeChunkToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_chunk",
		Group:       ToolGroupChunkedWrite,
		Description: "Append indexed content to a chunked write session. " + chunkedWriteGuidePointer,
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
				"index":    map[string]any{"type": "integer"},
				"content":  map[string]any{"type": "string"},
				"sha256":   map[string]any{"type": "string"},
			},
			"required": []string{"write_id", "index", "content"},
		},
	}
}

func writeCommitToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_commit",
		Group:       ToolGroupChunkedWrite,
		Description: "Validate and atomically commit a chunked write. " + chunkedWriteGuidePointer,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id":        map[string]any{"type": "string"},
				"expected_chunks": map[string]any{"type": "integer"},
				"sha256":          map[string]any{"type": "string"},
			},
			"required": []string{"write_id"},
		},
	}
}

func writeAbortToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_abort",
		Group:       ToolGroupChunkedWrite,
		Description: "Abort and discard an unfinished chunked write. " + chunkedWriteGuidePointer,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
			},
			"required": []string{"write_id"},
		},
	}
}
