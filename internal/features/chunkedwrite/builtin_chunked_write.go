package chunkedwrite

import (
	"fmt"
	"path/filepath"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func Contributions(ctx Options) []toolcore.Tool {
	if ctx.WorkDir != "" {
		if abs, err := filepath.Abs(ctx.WorkDir); err == nil {
			ctx.WorkDir = abs
		}
	}
	manager := ctx.ChunkedWrites
	if manager == nil {
		manager = newChunkWriteManager(ctx.WorkDir, ctx.FilePolicy)
	}
	return []toolcore.Tool{
		writeBeginTool(manager),
		writeChunkTool(manager),
		writeCommitTool(manager),
		writeAbortTool(manager),
	}
}

func writeBeginToolDefinition() toolcore.ToolDefinition {
	return toolcore.ToolDefinition{
		Name:        "write_begin",
		Group:       toolcore.ToolGroupChunkedWrite,
		Guide:       toolcore.ToolGuide{Loader: "skill_load", Name: "juex-chunked-write"},
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

func writeChunkToolDefinition() toolcore.ToolDefinition {
	return toolcore.ToolDefinition{
		Name:                      "write_chunk",
		MalformedArgumentsMessage: fmt.Sprintf("provider returned malformed tool arguments; retry with smaller write_chunk content, preferably no more than %d chars or %d bytes per chunk", ChunkWriteRecommendedChunkChars, ChunkWriteRecommendedChunkBytes),
		Group:                     toolcore.ToolGroupChunkedWrite,
		Guide:                     toolcore.ToolGuide{Loader: "skill_load", Name: "juex-chunked-write"},
		Description:               fmt.Sprintf("Add write_id content. Index starts at 0; identical retries are safe. Max %d chars and %d bytes. Optional sha256 verifies content.", chunkWriteMaxChunkChars, chunkWriteMaxChunkBytes),
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

func writeCommitToolDefinition() toolcore.ToolDefinition {
	return toolcore.ToolDefinition{
		Name:        "write_commit",
		Group:       toolcore.ToolGroupChunkedWrite,
		Guide:       toolcore.ToolGuide{Loader: "skill_load", Name: "juex-chunked-write"},
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

func writeAbortToolDefinition() toolcore.ToolDefinition {
	return toolcore.ToolDefinition{
		Name:        "write_abort",
		Group:       toolcore.ToolGroupChunkedWrite,
		Guide:       toolcore.ToolGuide{Loader: "skill_load", Name: "juex-chunked-write"},
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
