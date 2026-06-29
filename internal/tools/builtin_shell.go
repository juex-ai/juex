package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ShellToolProvider struct{}

func (ShellToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	return []Tool{
		execCommandTool(ctx.WorkDir, ctx.Shell, ctx.ShellSessions, ctx.ToolTimeoutSeconds),
		writeStdinTool(ctx.ShellSessions),
	}
}

func execCommandTool(defaultWorkdir string, profile ShellProfile, sessions *ShellSessionManager, timeoutSeconds int) Tool {
	return Tool{
		Name:           "exec_command",
		Description:    execCommandToolDescription(profile),
		TimeoutSeconds: timeoutSeconds,
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
			},
			"required": []string{"cmd"},
		},
		ResultHandler: func(ctx context.Context, in map[string]any) (Result, error) {
			cmd, _ := in["cmd"].(string)
			if cmd == "" {
				return Result{}, fmt.Errorf("exec_command: missing cmd")
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
				Timeout:         time.Duration(timeoutSeconds) * time.Second,
				Yield:           yield,
				MaxOutputTokens: maxOutputTokens,
				TTY:             tty,
				CallContext:     ctx,
				Events:          ToolCallEventsFromContext(ctx),
			})
			if err != nil {
				return Result{}, err
			}
			out := shellToolResult(result)
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
		ResultHandler: func(ctx context.Context, in map[string]any) (Result, error) {
			sessionID, ok := toInt(in["session_id"])
			if !ok || sessionID <= 0 {
				return Result{}, fmt.Errorf("write_stdin: missing session_id")
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
			out := shellToolResult(result)
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

func shellToolResult(result ShellSessionResult) Result {
	return Result{
		Text:       formatShellSessionResult(result),
		Structured: NewShellResult(result),
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
