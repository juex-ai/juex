package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/sandbox"
)

type FileToolProvider struct{}

func (FileToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	guard := sandbox.NewPathGuard(ctx.WorkDir, ctx.Sandbox)
	out := []Tool{
		readTool(ctx.WorkDir, guard),
		writeTool(ctx.WorkDir, guard),
		editTool(ctx.WorkDir, guard),
	}
	if !ctx.Options.DisableApplyPatch {
		out = append(out, applyPatchTool(ctx.WorkDir, guard))
	}
	return out
}

func readTool(workDir string, guard sandbox.PathGuard) Tool {
	return Tool{
		Name:        "read",
		Description: "Read a UTF-8 text file or image. Text reads return file contents with optional offset (1-based line) and limit (max lines). Image reads return a media reference visible to vision-capable models; offset and limit are not supported for images.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Absolute or working-dir-relative path"},
				"offset": map[string]any{"type": "integer", "description": "1-based line to start at"},
				"limit":  map[string]any{"type": "integer", "description": "Max number of lines to return"},
			},
			"required": []string{"path"},
		},
		ResultHandler: func(ctx context.Context, in map[string]any) (Result, error) {
			path, _ := in["path"].(string)
			if path == "" {
				return Result{}, fmt.Errorf("read: missing path")
			}
			path = resolveWorkPath(workDir, path)
			if err := guard.Check(path); err != nil {
				return Result{}, fmt.Errorf("read: %w", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return Result{}, err
			}
			offset, _ := toInt(in["offset"])
			limit, _ := toInt(in["limit"])
			if kind, ok := detectReadImage(path, data); ok {
				if offset > 0 || limit > 0 {
					return Result{}, fmt.Errorf("read: offset and limit are not supported for image files")
				}
				return readImageResult(workDir, data, kind)
			}
			if offset <= 0 && limit <= 0 {
				return Result{Text: string(data)}, nil
			}
			lines := strings.Split(string(data), "\n")
			start := 0
			if offset > 0 {
				start = offset - 1
			}
			if start > len(lines) {
				start = len(lines)
			}
			end := len(lines)
			if limit > 0 && start+limit < end {
				end = start + limit
			}
			return Result{Text: strings.Join(lines[start:end], "\n")}, nil
		},
	}
}

func writeTool(workDir string, guard sandbox.PathGuard) Tool {
	return Tool{
		Name:        "write",
		Description: fmt.Sprintf("Write short content to a file, creating parent directories if needed. Overwrites existing files. For generated content longer than %d characters, use write_begin/write_chunk/write_commit instead.", chunkWriteRecommendedChunkChars),
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string", "maxLength": chunkWriteRecommendedChunkChars},
			},
			"required": []string{"path", "content"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			content, _ := in["content"].(string)
			if path == "" {
				return "", fmt.Errorf("write: missing path")
			}
			path = resolveWorkPath(workDir, path)
			if err := guard.Check(path); err != nil {
				return "", fmt.Errorf("write: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
		},
	}
}

func editTool(workDir string, guard sandbox.PathGuard) Tool {
	return Tool{
		Name:        "edit",
		Description: "Replace `old` with `new` in the file at `path`. By default `old` must appear exactly once; set replace_all to replace every occurrence and optionally expected_replacements to require an exact count.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":                  map[string]any{"type": "string"},
				"old":                   map[string]any{"type": "string"},
				"new":                   map[string]any{"type": "string"},
				"replace_all":           map[string]any{"type": "boolean", "description": "Replace every occurrence of old instead of requiring a unique match"},
				"expected_replacements": map[string]any{"type": "integer", "description": "If set, require exactly this many replacements before writing"},
			},
			"required": []string{"path", "old", "new"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			oldStr, _ := in["old"].(string)
			newStr, newOK := in["new"].(string)
			replaceAll, _ := in["replace_all"].(bool)
			var expected int
			var expectedSet bool
			if val, ok := in["expected_replacements"]; ok && val != nil {
				var parsed bool
				expected, parsed = toInt(val)
				if !parsed || expected <= 0 {
					return "", fmt.Errorf("edit: expected_replacements must be a positive integer")
				}
				expectedSet = true
			}
			if missing := missingEditRequiredArgs(path, oldStr, newOK); len(missing) > 0 {
				return "", fmt.Errorf("edit: missing required argument(s): %s (expected keys: path, old, new; received keys: %s)", strings.Join(missing, ", "), receivedArgumentKeys(in))
			}
			path = resolveWorkPath(workDir, path)
			if err := guard.Check(path); err != nil {
				return "", fmt.Errorf("edit: %w", err)
			}
			if expectedSet && expected != 1 && !replaceAll {
				return "", fmt.Errorf("edit: expected_replacements greater than 1 requires replace_all")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			content := string(data)
			count := strings.Count(content, oldStr)
			replacements := 1
			switch count {
			case 0:
				return "", fmt.Errorf("edit: %s: old string not found", path)
			case 1:
				content = strings.Replace(content, oldStr, newStr, 1)
			default:
				if !replaceAll {
					return "", fmt.Errorf("edit: %s: old string occurs %d times; need a unique match", path, count)
				}
				replacements = count
				content = strings.ReplaceAll(content, oldStr, newStr)
			}
			if expectedSet && count != expected {
				return "", fmt.Errorf("edit: %s: expected %d replacements, found %d", path, expected, count)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", err
			}
			replacementLabel := "replacement"
			if replacements != 1 {
				replacementLabel = "replacements"
			}
			return fmt.Sprintf("edited %s (%d %s)", path, replacements, replacementLabel), nil
		},
	}
}

func missingEditRequiredArgs(path, oldStr string, newOK bool) []string {
	missing := []string{}
	if path == "" {
		missing = append(missing, "path")
	}
	if oldStr == "" {
		missing = append(missing, "old")
	}
	if !newOK {
		missing = append(missing, "new")
	}
	return missing
}

func receivedArgumentKeys(in map[string]any) string {
	if len(in) == 0 {
		return "<none>"
	}
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func resolveWorkPath(workDir, path string) string {
	if path == "" || filepath.IsAbs(path) || workDir == "" {
		return path
	}
	return filepath.Join(workDir, path)
}
