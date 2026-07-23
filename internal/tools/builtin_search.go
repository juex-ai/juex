package tools

import (
	"context"
	"fmt"
	"regexp"

	"github.com/juex-ai/juex/internal/sandbox"
)

type SearchToolProvider struct{}

func (SearchToolProvider) definitions(BuiltinDefinitionOptions) []ToolDefinition {
	return []ToolDefinition{grepToolDefinition()}
}

func (SearchToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	runner := ctx.SearchRunner
	if runner == nil {
		runner = NewRipgrepRunner(RipgrepRunnerOptions{
			WorkDir:       ctx.WorkDir,
			Sandbox:       ctx.Sandbox,
			SandboxRunner: ctx.SandboxRunner,
		})
	}
	return []Tool{grepTool(ctx.WorkDir, sandbox.NewPathGuard(ctx.WorkDir, ctx.Sandbox), runner)}
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

func grepTool(defaultPath string, guard sandbox.PathGuard, runner SearchRunner) Tool {
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
		if _, err := regexp.Compile(pattern); err != nil {
			return "", fmt.Errorf("grep: bad pattern: %w", err)
		}
		if runner == nil {
			return "", fmt.Errorf("grep: search runner is unavailable")
		}
		result, err := runner.Grep(ctx, GrepRequest{Pattern: pattern, Path: path})
		return formatGrepResult(result), err
	})
}
