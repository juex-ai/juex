package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/sandbox"
)

type ShellToolProvider struct{}

func (ShellToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	return []Tool{
		execCommandTool(ctx.WorkDir, ctx.Shell, ctx.ShellSessions, ctx.Sandbox, ctx.SandboxRunner),
		listShellSessionsTool(ctx.ShellSessions),
		writeStdinTool(ctx.ShellSessions),
	}
}

func execCommandTool(defaultWorkdir string, profile ShellProfile, sessions *ShellSessionManager, sandboxPolicy sandbox.Policy, sandboxRunner sandbox.Runner) Tool {
	return Tool{
		Name:          "exec_command",
		Description:   execCommandToolDescription(profile),
		TimeoutPolicy: ToolTimeoutDisabled,
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
				WorkspaceRoots:  shellWorkspaceRoots(defaultWorkdir),
				Sandbox:         sandboxPolicy,
				SandboxRunner:   sandboxRunner,
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

func shellWorkspaceRoots(defaultWorkdir string) []string {
	if strings.TrimSpace(defaultWorkdir) == "" {
		return nil
	}
	return []string{defaultWorkdir}
}

func listShellSessionsTool(sessions *ShellSessionManager) Tool {
	return Tool{
		Name:        "list_shell_sessions",
		Description: "List JueX-managed exec_command shell sessions so you can recover session_id values. exec_command starts and observes commands, write_stdin polls or sends input to a known session_id, and list_shell_sessions finds active session ids. By default only running sessions are returned; set include_completed to inspect retained completed sessions.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"include_completed": map[string]any{
					"type":        "boolean",
					"description": "Include completed shell sessions retained in memory. Defaults to false.",
				},
			},
		},
		ResultHandler: func(ctx context.Context, in map[string]any) (Result, error) {
			includeCompleted, _ := in["include_completed"].(bool)
			result := ShellSessionListResult{Sessions: sessions.List(includeCompleted)}
			return Result{
				Text:       formatShellSessionList(result, includeCompleted),
				Structured: result,
			}, nil
		},
	}
}

func writeStdinTool(sessions *ShellSessionManager) Tool {
	return Tool{
		Name:          "write_stdin",
		Description:   "Poll a running exec_command session or write chars to a tty session. Use the numeric session_id returned by exec_command.",
		TimeoutPolicy: ToolTimeoutDisabled,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "integer"},
				"chars": map[string]any{
					"type":        "string",
					"description": "Characters to write before waiting. Non-empty writes require tty=true except Ctrl-C (\\x03), which interrupts non-TTY sessions.",
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

func formatShellSessionList(result ShellSessionListResult, includeCompleted bool) string {
	if len(result.Sessions) == 0 {
		if includeCompleted {
			return "No shell sessions."
		}
		return "No running shell sessions."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Shell sessions: %d\n", len(result.Sessions))
	for _, session := range result.Sessions {
		status := "running"
		if !session.Running {
			status = "exited"
			if session.TimedOut {
				status = "timed_out"
			}
		}
		fmt.Fprintf(
			&b,
			"session_id=%d status=%s tty=%t age=%s idle=%s chunk_id=%d unread_bytes=%d",
			session.SessionID,
			status,
			session.TTY,
			formatShellListDuration(session.AgeMS),
			formatShellListDuration(session.IdleMS),
			session.ChunkID,
			session.UnreadBytes,
		)
		if session.ExitCode != nil {
			fmt.Fprintf(&b, " exit_code=%d", *session.ExitCode)
		}
		if session.Workdir != "" {
			fmt.Fprintf(&b, " workdir=%q", session.Workdir)
		}
		fmt.Fprintf(&b, " command=%q\n", session.Command)
	}
	return strings.TrimRight(b.String(), "\n")
}

const (
	activeShellPromptMaxSessions = 8
	activeShellPromptMaxCommand  = 160
	activeShellPromptMaxWorkdir  = 120
)

func FormatActiveShellSessionsPrompt(sessions []ShellSessionInfo) string {
	active := make([]ShellSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if session.Running {
			active = append(active, session)
		}
	}
	if len(active) == 0 {
		return ""
	}
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].SessionID < active[j].SessionID
	})

	limit := len(active)
	if limit > activeShellPromptMaxSessions {
		limit = activeShellPromptMaxSessions
	}
	var b strings.Builder
	b.WriteString("## Active Shell Sessions\n")
	b.WriteString("These exec_command sessions are still running in this JueX process. Use `write_stdin` with `session_id` to poll output or send input; use `list_shell_sessions` for full current details. Sessions are not restored after JueX restarts.\n")
	for _, session := range active[:limit] {
		fmt.Fprintf(
			&b,
			"- session_id=%d running=true tty=%t age=%s idle=%s chunk_id=%d unread_bytes=%d workdir=%q command=%q\n",
			session.SessionID,
			session.TTY,
			formatShellListDuration(session.AgeMS),
			formatShellListDuration(session.IdleMS),
			session.ChunkID,
			session.UnreadBytes,
			truncateShellPromptField(session.Workdir, activeShellPromptMaxWorkdir),
			truncateShellPromptField(session.Command, activeShellPromptMaxCommand),
		)
	}
	if omitted := len(active) - limit; omitted > 0 {
		fmt.Fprintf(&b, "- %d more active shell session(s) omitted; call `list_shell_sessions` for the full list.\n", omitted)
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncateShellPromptField(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func formatShellListDuration(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	return (time.Duration(ms) * time.Millisecond).String()
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
