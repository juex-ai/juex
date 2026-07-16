package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/juex-ai/juex/internal/sandbox"
)

type SearchToolProvider struct{}

func (SearchToolProvider) definitions(BuiltinDefinitionOptions) []ToolDefinition {
	return []ToolDefinition{grepToolDefinition()}
}

func (SearchToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	return []Tool{grepTool(ctx.WorkDir, sandbox.NewPathGuard(ctx.WorkDir, ctx.Sandbox))}
}

func grepToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "grep",
		Group:       ToolGroupSearch,
		Description: "Recursively search for a Go-regexp pattern under `path` (file or directory). Output: `relative_path:line:content` (max 200 hits).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Go regexp"},
				"path":    map[string]any{"type": "string", "description": "File or directory; defaults to the agent WorkDir"},
			},
			"required": []string{"pattern"},
		},
	}
}

func grepTool(defaultPath string, guard sandbox.PathGuard) Tool {
	return grepToolDefinition().Bind(func(ctx context.Context, in map[string]any) (string, error) {
		pattern, _ := in["pattern"].(string)
		path, _ := in["path"].(string)
		if pattern == "" {
			return "", fmt.Errorf("grep: missing pattern")
		}
		if path == "" {
			if defaultPath != "" {
				path = defaultPath
			} else {
				path = "."
			}
		}
		path = resolveWorkPath(defaultPath, path)
		if err := guard.Check(path); err != nil {
			return "", fmt.Errorf("grep: %w", err)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("grep: bad pattern: %w", err)
		}

		var hits []string
		const maxHits = 200

		walk := func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if guard.IsBlocked(p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "node_modules" || name == ".agents" {
					return filepath.SkipDir
				}
				return nil
			}
			if len(hits) >= maxHits {
				return filepath.SkipAll
			}
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			rel, _ := filepath.Rel(path, p)
			if rel == "" {
				rel = p
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			lineNo := 0
			for scanner.Scan() {
				lineNo++
				line := scanner.Text()
				if re.MatchString(line) {
					hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
					if len(hits) >= maxHits {
						return filepath.SkipAll
					}
				}
			}
			return nil
		}

		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			if err := filepath.WalkDir(path, walk); err != nil {
				return "", err
			}
		} else {
			if err := walk(path, fileInfoEntry{info}, nil); err != nil {
				return "", err
			}
		}
		if len(hits) == 0 {
			return "(no matches)", nil
		}
		return strings.Join(hits, "\n"), nil
	})
}

// fileInfoEntry adapts os.FileInfo to fs.DirEntry for the single-file case.
type fileInfoEntry struct{ os.FileInfo }

func (f fileInfoEntry) Type() os.FileMode          { return f.Mode().Type() }
func (f fileInfoEntry) Info() (os.FileInfo, error) { return f.FileInfo, nil }
