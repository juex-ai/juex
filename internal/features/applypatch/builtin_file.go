package applypatch

import (
	"path/filepath"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func Contributions(ctx Options) []toolcore.Tool {
	if ctx.WorkDir != "" {
		if abs, err := filepath.Abs(ctx.WorkDir); err == nil {
			ctx.WorkDir = abs
		}
	}
	return []toolcore.Tool{applyPatchTool(ctx.WorkDir, ctx.FilePolicy)}
}

func applyPatchToolDefinition() toolcore.ToolDefinition {
	return toolcore.ToolDefinition{
		Name:        "apply_patch",
		Group:       toolcore.ToolGroupFile,
		Description: "Apply a Codex-style patch inside the workspace. File and move paths may be workspace-relative or absolute paths inside the workspace. Supports add, update, delete, and move operations. Input is {patch_text: \"*** Begin Patch\\n...\\n*** End Patch\"}.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch_text": map[string]any{
					"type":        "string",
					"description": "Codex-style patch text with *** Begin Patch / *** End Patch envelope.",
				},
			},
			"required": []string{"patch_text"},
		},
	}
}
