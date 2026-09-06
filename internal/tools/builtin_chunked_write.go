package tools

import "fmt"

type ChunkedWriteToolProvider struct{}

func (ChunkedWriteToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	manager := ctx.ChunkedWrites
	if manager == nil {
		manager = newChunkWriteManager(ctx.WorkDir, ctx.FilePolicy)
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
		Description: "Begin mode overwrite (default) or create (new file). Use write_id with write_chunk, then write_commit or write_abort.",
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
		Description: fmt.Sprintf("Add write_id content. Index starts at 0; identical retries are safe. Max %d chars and %d bytes. Optional sha256 verifies content.", chunkWriteMaxChunkChars, chunkWriteMaxChunkBytes),
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
		Description: "Publish write_id atomically. expected_chunks and sha256 optionally validate content; failure preserves session and target.",
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
		Description: "Discard buffered chunks; target unchanged.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
			},
			"required": []string{"write_id"},
		},
	}
}
