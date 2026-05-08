package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RegisterBuiltins adds the builtin tool set: read / write / edit / bash / grep.
//
// workDir is the default working directory used when bash / grep are
// invoked without an explicit cwd / path. Pass "" to fall back to the
// process cwd (bash) and "." (grep) respectively.
func RegisterBuiltins(r *Registry, workDir string) {
	r.MustRegister(readTool())
	r.MustRegister(writeTool())
	r.MustRegister(editTool())
	r.MustRegister(bashTool(workDir))
	r.MustRegister(grepTool(workDir))
}

// ----- read -----

func readTool() Tool {
	return Tool{
		Name:        "read",
		Description: "Read a UTF-8 text file. Returns the file contents. Optional offset (1-based line) and limit (max lines).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Absolute or working-dir-relative path"},
				"offset": map[string]any{"type": "integer", "description": "1-based line to start at"},
				"limit":  map[string]any{"type": "integer", "description": "Max number of lines to return"},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			if path == "" {
				return "", fmt.Errorf("read: missing path")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			offset, _ := toInt(in["offset"])
			limit, _ := toInt(in["limit"])
			if offset <= 0 && limit <= 0 {
				return string(data), nil
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
			return strings.Join(lines[start:end], "\n"), nil
		},
	}
}

// ----- write -----

func writeTool() Tool {
	return Tool{
		Name:        "write",
		Description: "Write content to a file, creating parent directories if needed. Overwrites existing files.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			content, _ := in["content"].(string)
			if path == "" {
				return "", fmt.Errorf("write: missing path")
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

// ----- edit -----

func editTool() Tool {
	return Tool{
		Name:        "edit",
		Description: "Replace exactly one occurrence of `old` with `new` in the file at `path`. Errors if `old` is not found or is ambiguous.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"old":  map[string]any{"type": "string"},
				"new":  map[string]any{"type": "string"},
			},
			"required": []string{"path", "old", "new"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			oldStr, _ := in["old"].(string)
			newStr, _ := in["new"].(string)
			if path == "" || oldStr == "" {
				return "", fmt.Errorf("edit: path and old required")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			content := string(data)
			count := strings.Count(content, oldStr)
			switch count {
			case 0:
				return "", fmt.Errorf("edit: %s: old string not found", path)
			case 1:
				content = strings.Replace(content, oldStr, newStr, 1)
			default:
				return "", fmt.Errorf("edit: %s: old string occurs %d times; need a unique match", path, count)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("edited %s", path), nil
		},
	}
}

// ----- bash -----

func bashTool(defaultCwd string) Tool {
	return Tool{
		Name:        "bash",
		Description: "Run a shell command via `bash -c`. Returns stdout+stderr. Optional cwd and timeout (seconds, default 60).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cmd":     map[string]any{"type": "string"},
				"cwd":     map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer", "description": "Seconds, default 60"},
			},
			"required": []string{"cmd"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			cmd, _ := in["cmd"].(string)
			if cmd == "" {
				return "", fmt.Errorf("bash: missing cmd")
			}
			cwd, _ := in["cwd"].(string)
			if cwd == "" {
				cwd = defaultCwd
			}
			timeoutSec, _ := toInt(in["timeout"])
			if timeoutSec <= 0 {
				timeoutSec = 60
			}
			c, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
			defer cancel()
			ec := exec.CommandContext(c, "bash", "-c", cmd)
			if cwd != "" {
				ec.Dir = cwd
			}
			out, err := ec.CombinedOutput()
			if err != nil {
				return fmt.Sprintf("exit error: %s\n%s", err.Error(), string(out)), nil
			}
			return string(out), nil
		},
	}
}

// ----- grep -----

func grepTool(defaultPath string) Tool {
	return Tool{
		Name:        "grep",
		Description: "Recursively search for a Go-regexp pattern under `path` (file or directory). Output: `relative_path:line:content` (max 200 hits).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Go regexp"},
				"path":    map[string]any{"type": "string", "description": "File or directory; defaults to the agent WorkDir"},
			},
			"required": []string{"pattern"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
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
				defer func() { _ = f.Close() }()
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
		},
	}
}

// fileInfoEntry adapts os.FileInfo to fs.DirEntry for the single-file case.
type fileInfoEntry struct{ os.FileInfo }

func (f fileInfoEntry) Type() os.FileMode          { return f.Mode().Type() }
func (f fileInfoEntry) Info() (os.FileInfo, error) { return f.FileInfo, nil }

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case nil:
		return 0, false
	}
	return 0, false
}
