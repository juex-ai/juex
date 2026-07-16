package tools

import (
	"fmt"

	"github.com/juex-ai/juex/internal/sandbox"
)

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
		Description: fmt.Sprintf("Begin a chunked full-file write session for a long generated file. Use write_chunk repeatedly with small provider-safe chunks, preferably no more than %d characters or %d bytes each, then write_commit to atomically create or overwrite the final file.", chunkWriteRecommendedChunkChars, chunkWriteRecommendedChunkBytes),
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Working-dir-relative target file path"},
				"mode": map[string]any{"type": "string", "description": "overwrite (default) or create"},
			},
			"required": []string{"path"},
		},
	}
}

func writeChunkToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_chunk",
		Group:       ToolGroupChunkedWrite,
		Description: fmt.Sprintf("Record one chunk for a chunked write session. Send the actual content string in content. For long files, split content across multiple sequential write_chunk calls, preferably no more than %d characters or %d bytes per chunk. Do not send summary or size metadata such as content_omitted, content_bytes, content_chars, or content_sha256 as input; those fields are not file content. The result is a compact acknowledgement and never echoes content.", chunkWriteRecommendedChunkChars, chunkWriteRecommendedChunkBytes),
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
				"index":    map[string]any{"type": "integer", "description": "Zero-based chunk index"},
				"content": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("Actual chunk text. Keep each call <= %d characters and <= %d bytes; continue with the next index for more content.", chunkWriteRecommendedChunkChars, chunkWriteRecommendedChunkBytes),
					"maxLength":   chunkWriteRecommendedChunkChars,
				},
				"sha256": map[string]any{"type": "string", "description": "Optional SHA-256 hex digest of this chunk"},
			},
			"required": []string{"write_id", "index", "content"},
		},
	}
}

func writeCommitToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_commit",
		Group:       ToolGroupChunkedWrite,
		Description: "Validate and commit a chunked write session to its final file using a temporary file plus rename.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id":        map[string]any{"type": "string"},
				"expected_chunks": map[string]any{"type": "integer", "description": "Optional expected number of chunks"},
				"sha256":          map[string]any{"type": "string", "description": "Optional SHA-256 hex digest of the assembled content"},
			},
			"required": []string{"write_id"},
		},
	}
}

func writeAbortToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "write_abort",
		Group:       ToolGroupChunkedWrite,
		Description: "Abort and discard an unfinished chunked write session.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
			},
			"required": []string{"write_id"},
		},
	}
}
