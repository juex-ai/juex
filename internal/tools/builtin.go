package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type BuiltinOptions struct {
	WorkDir       string
	Shell         ShellProfile
	ShellSessions *ShellSessionManager
}

type ShellProfile struct {
	Profile       string
	Family        string
	Binary        string
	Args          []string
	PathStyle     string
	HostPathStyle string
}

// RegisterBuiltins adds the builtin tool set: read / write / edit / exec_command / write_stdin / grep.
//
// workDir is the default working directory used for relative file paths and
// for exec_command / grep calls without an explicit workdir / path. Pass "" to fall back
// to the process cwd (file tools and shell) and "." (grep).
func RegisterBuiltins(r *Registry, opts BuiltinOptions) {
	workDir := opts.WorkDir
	if workDir != "" {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}
	shell := opts.Shell
	if shell.Binary == "" {
		shell = DefaultShellProfile()
	}
	shellSessions := opts.ShellSessions
	if shellSessions == nil {
		shellSessions = NewShellSessionManager(context.Background())
	}
	r.MustRegister(readTool(workDir))
	r.MustRegister(writeTool(workDir))
	r.MustRegister(editTool(workDir))
	r.MustRegister(execCommandTool(workDir, shell, shellSessions))
	r.MustRegister(writeStdinTool(shellSessions))
	r.MustRegister(grepTool(workDir))
}

func DefaultShellProfile() ShellProfile {
	if runtime.GOOS == "windows" {
		return ShellProfile{
			Profile:   "cmd",
			Family:    "cmd",
			Binary:    "cmd.exe",
			Args:      []string{"/c"},
			PathStyle: "windows",
		}
	}
	return ShellProfile{
		Profile:   "sh",
		Family:    "posix",
		Binary:    "sh",
		Args:      []string{"-c"},
		PathStyle: "posix",
	}
}

// ----- read -----

func readTool(workDir string) Tool {
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
			path = resolveWorkPath(workDir, path)
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

func writeTool(workDir string) Tool {
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
			path = resolveWorkPath(workDir, path)
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

func editTool(workDir string) Tool {
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
			newStr, _ := in["new"].(string)
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
			if path == "" || oldStr == "" {
				return "", fmt.Errorf("edit: path and old required")
			}
			path = resolveWorkPath(workDir, path)
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

func resolveWorkPath(workDir, path string) string {
	if path == "" || filepath.IsAbs(path) || workDir == "" {
		return path
	}
	return filepath.Join(workDir, path)
}

// ----- exec_command / write_stdin -----

func execCommandTool(defaultWorkdir string, profile ShellProfile, sessions *ShellSessionManager) Tool {
	return Tool{
		Name:        "exec_command",
		Description: execCommandToolDescription(profile),
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cmd":     map[string]any{"type": "string"},
				"workdir": map[string]any{"type": "string"},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Wait this many milliseconds before returning. Defaults to 10000.",
					"minimum":     int(minShellYield / time.Millisecond),
					"maximum":     int(maxShellYield / time.Millisecond),
				},
				"max_output_tokens": map[string]any{
					"type":        "integer",
					"description": "Approximate maximum output tokens returned in this tool result.",
					"minimum":     1,
				},
				"tty": map[string]any{
					"type":        "boolean",
					"description": "Allocate a pseudo-terminal for interactive commands. Use write_stdin chars to continue the session.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Seconds to allow this command to run. Defaults to 60 and is capped at 300.",
					"minimum":     1,
					"maximum":     MaxTimeoutSeconds,
				},
			},
			"required": []string{"cmd"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			cmd, _ := in["cmd"].(string)
			if cmd == "" {
				return "", fmt.Errorf("exec_command: missing cmd")
			}
			workdir, _ := in["workdir"].(string)
			if workdir == "" {
				workdir = defaultWorkdir
			} else {
				workdir = resolveWorkPath(defaultWorkdir, workdir)
			}
			tty, _ := in["tty"].(bool)
			maxOutputTokens := defaultMaxOutputTokens(in)
			yield := defaultShellExecYield
			if yieldMS, ok := positiveInt(in["yield_time_ms"]); ok {
				yield = time.Duration(yieldMS) * time.Millisecond
			}
			result, err := sessions.Start(ShellStartRequest{
				Binary:          profile.Binary,
				Args:            profile.Args,
				Command:         cmd,
				Cwd:             workdir,
				Timeout:         time.Duration(CallTimeoutSeconds(in)) * time.Second,
				Yield:           yield,
				MaxOutputTokens: maxOutputTokens,
				TTY:             tty,
				CallContext:     ctx,
				Events:          ToolCallEventsFromContext(ctx),
			})
			if err != nil {
				return "", err
			}
			out := formatShellSessionResult(result)
			if err := shellSessionExitError("exec_command", result); err != nil {
				return out, err
			}
			return out, nil
		},
	}
}

func writeStdinTool(sessions *ShellSessionManager) Tool {
	return Tool{
		Name:        "write_stdin",
		Description: "Poll a running exec_command session or write chars to a tty session. Use the numeric session_id returned by exec_command.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "integer"},
				"chars": map[string]any{
					"type":        "string",
					"description": "Characters to write before waiting. Non-empty writes require the exec_command session to have been started with tty=true.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Milliseconds to wait for more output. Non-empty writes default to 250 and cap at 30000; empty polls default to 5000 and cap at 300000.",
					"minimum":     int(minShellYield / time.Millisecond),
					"maximum":     int(maxShellInputPollYield / time.Millisecond),
				},
				"max_output_tokens": map[string]any{
					"type":        "integer",
					"description": "Approximate maximum output tokens returned in this tool result.",
					"minimum":     1,
				},
			},
			"required": []string{"session_id"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			sessionID, ok := toInt(in["session_id"])
			if !ok || sessionID <= 0 {
				return "", fmt.Errorf("write_stdin: missing session_id")
			}
			input, _ := in["chars"].(string)
			yield := defaultShellInputPollYield
			if input != "" {
				yield = defaultShellInputWriteYield
			}
			if yieldMS, ok := positiveInt(in["yield_time_ms"]); ok {
				yield = time.Duration(yieldMS) * time.Millisecond
			}
			maxOutputTokens := defaultMaxOutputTokens(in)
			result, err := sessions.Continue(ShellContinueRequest{
				SessionID:       sessionID,
				Stdin:           input,
				Yield:           yield,
				MaxOutputTokens: maxOutputTokens,
				CallContext:     ctx,
			})
			out := formatShellSessionResult(result)
			if err != nil {
				return out, err
			}
			if err := shellSessionExitError("write_stdin", result); err != nil {
				return out, err
			}
			return out, nil
		},
	}
}

func formatShellSessionResult(result ShellSessionResult) string {
	var b strings.Builder
	if result.ChunkID > 0 {
		fmt.Fprintf(&b, "Chunk ID: %d\n", result.ChunkID)
	}
	fmt.Fprintf(&b, "Wall time: %.4f seconds\n", result.WallTime.Seconds())
	if result.ExitCode != nil {
		fmt.Fprintf(&b, "Process exited with code %d\n", *result.ExitCode)
	}
	if result.Running && result.SessionID > 0 {
		fmt.Fprintf(&b, "Process running with session ID %d\n", result.SessionID)
	}
	fmt.Fprintf(&b, "Original token count: %d\n", result.OriginalTokenCount)
	b.WriteString("Output:\n")
	b.WriteString(result.Output)
	return b.String()
}

func execCommandToolDescription(profile ShellProfile) string {
	name := profile.Profile
	if name == "" {
		name = profile.Family
	}
	if name == "" {
		name = "configured"
	}
	binary := profile.Binary
	if binary == "" {
		binary = "shell"
	}
	style := profile.PathStyle
	if style == "" {
		style = "platform"
	}
	family := profile.Family
	if family == "" {
		family = name
	}
	return fmt.Sprintf("Run a command in the current workspace shell. Current shell: %s via `%s %s <cmd>`. Use %s syntax and %s paths. Returns stdout+stderr. Optional workdir, tty, yield_time_ms, and max_output_tokens.", name, binary, strings.Join(profile.Args, " "), family, style)
}

func defaultMaxOutputTokens(in map[string]any) int {
	maxOutputTokens, ok := toInt(in["max_output_tokens"])
	if !ok || maxOutputTokens <= 0 {
		return defaultShellMaxOutputTokens
	}
	return maxOutputTokens
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

func positiveInt(v any) (int, bool) {
	n, ok := toInt(v)
	return n, ok && n > 0
}
