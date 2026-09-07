// Package hooks implements trusted command hooks for runtime lifecycle events.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	hookconfig "github.com/juex-ai/juex/internal/features/hooks/config"
	"github.com/juex-ai/juex/internal/foundation/environment"
)

// RuntimeContext carries private execution paths for a command hook supplied
// by an Extension. It stays out of hooks.yaml and is attached by App assembly.
type RuntimeContext struct {
	ExtensionDir            string
	ExtensionDataDir        string
	PrepareExtensionDataDir func() error
}

type Request struct {
	EventName             hookconfig.EventName `json:"event_name"`
	ThreadID              string               `json:"thread_id,omitempty"`
	TurnID                string               `json:"turn_id,omitempty"`
	CWD                   string               `json:"cwd,omitempty"`
	WorkspaceRoots        []string             `json:"workspace_roots,omitempty"`
	PermissionMode        string               `json:"permission_mode,omitempty"`
	SandboxMode           string               `json:"sandbox_mode,omitempty"`
	GenerationJournalPath string               `json:"generation_journal_path,omitempty"`
	ToolName              string               `json:"tool_name,omitempty"`
	ToolInput             map[string]any       `json:"tool_input,omitempty"`
	ToolResult            string               `json:"tool_result,omitempty"`
	UserInput             string               `json:"user_input,omitempty"`
	CompactReason         string               `json:"compact_reason,omitempty"`
	CompactAuto           bool                 `json:"compact_auto,omitempty"`
	GoalState             json.RawMessage      `json:"goal_state,omitempty"`
	Observer              Observer             `json:"-"`
}

type Result struct {
	Hook      hookconfig.CommandHook
	EventName hookconfig.EventName
	ToolName  string
	ExitCode  int
	Stdout    string
	Stderr    string
	Duration  time.Duration
}

type Observer interface {
	HookStarted(hookconfig.CommandHook, Request)
	HookCompleted(Result)
	HookErrored(Result, error)
}

type Runner struct {
	hooks           []hookconfig.CommandHook
	environment     environment.Snapshot
	runtimeContexts map[string]RuntimeContext
}

func NewRunner(cfg hookconfig.Config) (*Runner, error) {
	return NewRunnerWithOptions(cfg, RunnerOptions{})
}

type RunnerOptions struct {
	Environment     environment.Snapshot
	RuntimeContexts map[string]RuntimeContext
}

func NewRunnerWithOptions(cfg hookconfig.Config, opts RunnerOptions) (*Runner, error) {
	hooks := append([]hookconfig.CommandHook(nil), cfg.Commands...)
	for i := range hooks {
		if err := hookconfig.ValidateHook(hooks[i]); err != nil {
			return nil, err
		}
	}
	contexts := make(map[string]RuntimeContext, len(opts.RuntimeContexts))
	for name, binding := range opts.RuntimeContexts {
		contexts[name] = binding
	}
	return &Runner{hooks: hooks, environment: opts.Environment, runtimeContexts: contexts}, nil
}

func (r *Runner) Empty() bool {
	return r == nil || len(r.hooks) == 0
}

func (r *Runner) Matching(event hookconfig.EventName, toolName string) []hookconfig.CommandHook {
	if r == nil {
		return nil
	}
	var out []hookconfig.CommandHook
	for _, hook := range r.hooks {
		if hook.Matches(event, toolName) {
			out = append(out, hook)
		}
	}
	return out
}

func (r *Runner) Run(ctx context.Context, req Request) ([]Result, error) {
	if r == nil {
		return nil, nil
	}
	matches := r.Matching(req.EventName, req.ToolName)
	results := make([]Result, 0, len(matches))
	for _, hook := range matches {
		if req.Observer != nil {
			req.Observer.HookStarted(hook, req)
		}
		result, err := runCommandHook(ctx, hook, r.runtimeContexts[hook.Name], req, r.environment)
		if err != nil {
			if req.Observer != nil {
				req.Observer.HookErrored(result, err)
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return results, ctxErr
			}
			if hook.Required {
				return results, err
			}
			continue
		}
		results = append(results, result)
		if req.Observer != nil {
			req.Observer.HookCompleted(result)
		}
	}
	return results, nil
}

func runCommandHook(parent context.Context, hook hookconfig.CommandHook, binding RuntimeContext, req Request, snapshot environment.Snapshot) (Result, error) {
	start := time.Now()
	result := Result{Hook: hook, EventName: req.EventName, ToolName: req.ToolName}
	timeout := hook.TimeoutSeconds
	if timeout <= 0 {
		timeout = hookconfig.DefaultTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()

	input, err := json.Marshal(req)
	if err != nil {
		return result, fmt.Errorf("hooks: encode input for %q: %w", hook.Name, err)
	}
	commandLine, reserved, extension, err := prepareCommandRuntime(hook, binding, req.CWD)
	if err != nil {
		return result, err
	}
	command, err := snapshot.LookPathInDir(commandLine[0], req.CWD)
	if err != nil {
		return result, fmt.Errorf("hooks: %s executable %q: %w", hook.Name, commandLine[0], err)
	}
	if binding.ExtensionDataDir != "" {
		if binding.PrepareExtensionDataDir == nil {
			return result, fmt.Errorf("hooks: %s extension data directory has no prepare callback", hook.Name)
		}
		if err := parent.Err(); err != nil {
			return result, err
		}
		if err := binding.PrepareExtensionDataDir(); err != nil {
			return result, fmt.Errorf("hooks: %s prepare extension data directory: %w", hook.Name, err)
		}
	}
	cmd := exec.CommandContext(ctx, command, commandLine[1:]...)
	if req.CWD != "" {
		cmd.Dir = req.CWD
	}
	cmd.Env = snapshot.Environ(reserved)
	if !extension {
		cmd.Env = stripExtensionEnvironment(cmd.Env)
	}
	cmd.Stdin = bytes.NewReader(input)
	limit := hook.MaxOutputBytes
	if limit <= 0 {
		limit = hookconfig.DefaultMaxOutputBytes
	}
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	result.Duration = time.Since(start)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if err := parent.Err(); err != nil {
		return result, err
	}
	if stdout.exceeded {
		return result, fmt.Errorf("hooks: %s stdout exceeded %d bytes", hook.Name, limit)
	}
	if stderr.exceeded {
		return result, fmt.Errorf("hooks: %s stderr exceeded %d bytes", hook.Name, limit)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("hooks: %s timed out after %ds", hook.Name, timeout)
	}
	if err != nil {
		if exitErr != nil {
			if result.ExitCode == 2 {
				return result, nil
			}
			return result, &commandExitError{
				hookName: hook.Name,
				exitCode: result.ExitCode,
				stderr:   result.Stderr,
			}
		}
		return result, fmt.Errorf("hooks: %s failed: %w%s", hook.Name, err, stderrSuffix(result.Stderr))
	}
	return result, nil
}

var hookRuntimeVariablePattern = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)

func prepareCommandRuntime(hook hookconfig.CommandHook, binding RuntimeContext, workDir string) ([]string, map[string]string, bool, error) {
	command := append([]string(nil), hook.Command...)
	extension := strings.TrimSpace(binding.ExtensionDir) != ""
	reserved := map[string]string{
		"WORKDIR":      workDir,
		"JUEX_WORKDIR": workDir,
	}
	if extension {
		reserved["JUEX_EXT_DIR"] = binding.ExtensionDir
		reserved["JUEX_EXT_DATA_DIR"] = binding.ExtensionDataDir
	}
	for index, value := range command {
		expanded, err := expandRuntimeValue(value, reserved, extension)
		if err != nil {
			return nil, nil, false, fmt.Errorf("hooks: %s command: %w", hook.Name, err)
		}
		command[index] = expanded
	}
	return command, reserved, extension, nil
}

func expandRuntimeValue(value string, variables map[string]string, extension bool) (string, error) {
	var expansionErr error
	out := hookRuntimeVariablePattern.ReplaceAllStringFunc(value, func(token string) string {
		matches := hookRuntimeVariablePattern.FindStringSubmatch(token)
		name := matches[1]
		if name == "" {
			name = matches[2]
		}
		switch name {
		case "JUEX_EXT_DIR", "JUEX_EXT_DATA_DIR":
			if !extension {
				expansionErr = fmt.Errorf("%s is only available to extension definitions", name)
				return token
			}
			resolved := variables[name]
			if resolved == "" {
				expansionErr = fmt.Errorf("%s is unavailable for this extension definition", name)
				return token
			}
			return resolved
		default:
			if resolved, ok := variables[name]; ok {
				return resolved
			}
			return token
		}
	})
	return out, expansionErr
}

func stripExtensionEnvironment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if strings.EqualFold(key, "JUEX_EXT_DIR") || strings.EqualFold(key, "JUEX_EXT_DATA_DIR") {
			continue
		}
		out = append(out, item)
	}
	return out
}

type commandExitError struct {
	hookName string
	exitCode int
	stderr   string
}

func (e *commandExitError) Error() string {
	return fmt.Sprintf("hooks: %s exited with code %d%s", e.hookName, e.exitCode, stderrSuffix(e.stderr))
}

func stderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
