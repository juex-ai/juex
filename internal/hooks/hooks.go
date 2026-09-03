// Package hooks implements trusted command hooks for runtime lifecycle events.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/juex-ai/juex/internal/environment"
)

const (
	DefaultTimeoutSeconds = 10
	MaxTimeoutSeconds     = 300
	DefaultMaxOutputBytes = 64 * 1024
)

type EventName string

const (
	EventThreadStart      EventName = "ThreadStart"
	EventUserPromptSubmit EventName = "UserPromptSubmit"
	EventPreToolUse       EventName = "PreToolUse"
	EventPostToolUse      EventName = "PostToolUse"
	EventPreCompact       EventName = "PreCompact"
	EventPostCompact      EventName = "PostCompact"
	EventStop             EventName = "Stop"
)

type Config struct {
	Commands []CommandHook `json:"commands" yaml:"commands"`
}

type FileConfig struct {
	Trusted  bool          `yaml:"trusted"`
	Commands []CommandHook `yaml:"commands"`
}

type CommandHook struct {
	Name           string         `json:"name" yaml:"name"`
	Events         []EventName    `json:"events" yaml:"events"`
	Tools          []string       `json:"tools,omitempty" yaml:"tools"`
	Command        []string       `json:"command" yaml:"command"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty" yaml:"timeout_seconds"`
	MaxOutputBytes int            `json:"max_output_bytes,omitempty" yaml:"max_output_bytes"`
	Required       bool           `json:"required,omitempty" yaml:"required"`
	Source         string         `json:"source,omitempty" yaml:"-"`
	Runtime        RuntimeContext `json:"-" yaml:"-"`
}

// RuntimeContext carries private execution paths for a command hook supplied
// by an Extension. It stays out of hooks.yaml and is attached by App assembly.
type RuntimeContext struct {
	ExtensionDir            string
	ExtensionDataDir        string
	PrepareExtensionDataDir func() error
}

func (h CommandHook) Matches(event EventName, toolName string) bool {
	if len(h.Events) == 0 {
		return false
	}
	matchesEvent := false
	for _, candidate := range h.Events {
		if candidate == event {
			matchesEvent = true
			break
		}
	}
	if !matchesEvent {
		return false
	}
	if len(h.Tools) == 0 {
		return true
	}
	for _, tool := range h.Tools {
		if tool == toolName {
			return true
		}
	}
	return false
}

type Request struct {
	EventName             EventName       `json:"event_name"`
	ThreadID              string          `json:"thread_id,omitempty"`
	TurnID                string          `json:"turn_id,omitempty"`
	CWD                   string          `json:"cwd,omitempty"`
	WorkspaceRoots        []string        `json:"workspace_roots,omitempty"`
	PermissionMode        string          `json:"permission_mode,omitempty"`
	SandboxMode           string          `json:"sandbox_mode,omitempty"`
	GenerationJournalPath string          `json:"generation_journal_path,omitempty"`
	ToolName              string          `json:"tool_name,omitempty"`
	ToolInput             map[string]any  `json:"tool_input,omitempty"`
	ToolResult            string          `json:"tool_result,omitempty"`
	UserInput             string          `json:"user_input,omitempty"`
	CompactReason         string          `json:"compact_reason,omitempty"`
	CompactAuto           bool            `json:"compact_auto,omitempty"`
	GoalState             json.RawMessage `json:"goal_state,omitempty"`
	Observer              Observer        `json:"-"`
}

type Result struct {
	Hook      CommandHook
	EventName EventName
	ToolName  string
	ExitCode  int
	Stdout    string
	Stderr    string
	Duration  time.Duration
}

type Observer interface {
	HookStarted(CommandHook, Request)
	HookCompleted(Result)
	HookErrored(Result, error)
}

type Runner struct {
	hooks       []CommandHook
	environment environment.Snapshot
}

func NewRunner(cfg Config) (*Runner, error) {
	return NewRunnerWithOptions(cfg, RunnerOptions{})
}

type RunnerOptions struct {
	Environment environment.Snapshot
}

func NewRunnerWithOptions(cfg Config, opts RunnerOptions) (*Runner, error) {
	hooks := append([]CommandHook(nil), cfg.Commands...)
	for i := range hooks {
		if err := validateHook(hooks[i]); err != nil {
			return nil, err
		}
	}
	return &Runner{hooks: hooks, environment: opts.Environment}, nil
}

func (r *Runner) Empty() bool {
	return r == nil || len(r.hooks) == 0
}

func (r *Runner) Matching(event EventName, toolName string) []CommandHook {
	if r == nil {
		return nil
	}
	var out []CommandHook
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
		result, err := runCommandHook(ctx, hook, req, r.environment)
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

func ResolveFileConfig(fc FileConfig, source string, requireTrust bool) (Config, error) {
	if len(fc.Commands) == 0 {
		return Config{}, nil
	}
	if requireTrust && !fc.Trusted {
		return Config{}, fmt.Errorf("hooks: file command hooks require hooks.trusted: true")
	}
	cfg := Config{Commands: append([]CommandHook(nil), fc.Commands...)}
	for i := range cfg.Commands {
		cfg.Commands[i].Source = source
		if err := validateHook(cfg.Commands[i]); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func LoadFileConfig(path, source string, requireTrust bool) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var fc FileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("hooks: parse %s: %w", path, err)
	}
	return ResolveFileConfig(fc, source, requireTrust)
}

func runCommandHook(parent context.Context, hook CommandHook, req Request, snapshot environment.Snapshot) (Result, error) {
	start := time.Now()
	result := Result{Hook: hook, EventName: req.EventName, ToolName: req.ToolName}
	timeout := hook.TimeoutSeconds
	if timeout <= 0 {
		timeout = DefaultTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()

	input, err := json.Marshal(req)
	if err != nil {
		return result, fmt.Errorf("hooks: encode input for %q: %w", hook.Name, err)
	}
	commandLine, reserved, extension, err := prepareCommandRuntime(hook, req.CWD)
	if err != nil {
		return result, err
	}
	command, err := snapshot.LookPathInDir(commandLine[0], req.CWD)
	if err != nil {
		return result, fmt.Errorf("hooks: %s executable %q: %w", hook.Name, commandLine[0], err)
	}
	if hook.Runtime.ExtensionDataDir != "" {
		if hook.Runtime.PrepareExtensionDataDir == nil {
			return result, fmt.Errorf("hooks: %s extension data directory has no prepare callback", hook.Name)
		}
		if err := parent.Err(); err != nil {
			return result, err
		}
		if err := hook.Runtime.PrepareExtensionDataDir(); err != nil {
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
		limit = DefaultMaxOutputBytes
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

func prepareCommandRuntime(hook CommandHook, workDir string) ([]string, map[string]string, bool, error) {
	command := append([]string(nil), hook.Command...)
	extension := strings.TrimSpace(hook.Runtime.ExtensionDir) != ""
	reserved := map[string]string{
		"WORKDIR":      workDir,
		"JUEX_WORKDIR": workDir,
	}
	if extension {
		reserved["JUEX_EXT_DIR"] = hook.Runtime.ExtensionDir
		reserved["JUEX_EXT_DATA_DIR"] = hook.Runtime.ExtensionDataDir
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

func validateHook(h CommandHook) error {
	if strings.TrimSpace(h.Name) == "" {
		return fmt.Errorf("hooks: command hook name is required")
	}
	if len(h.Events) == 0 {
		return fmt.Errorf("hooks: %s: at least one event is required", h.Name)
	}
	for _, event := range h.Events {
		if !validEvent(event) {
			return fmt.Errorf("hooks: %s: invalid event %q", h.Name, event)
		}
	}
	if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
		return fmt.Errorf("hooks: %s: command is required", h.Name)
	}
	if h.TimeoutSeconds < 0 {
		return fmt.Errorf("hooks: %s: timeout_seconds must be >= 0", h.Name)
	}
	if h.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("hooks: %s: timeout_seconds cannot exceed %d seconds", h.Name, MaxTimeoutSeconds)
	}
	if h.MaxOutputBytes < 0 {
		return fmt.Errorf("hooks: %s: max_output_bytes must be >= 0", h.Name)
	}
	return nil
}

func validEvent(event EventName) bool {
	switch event {
	case EventThreadStart, EventUserPromptSubmit, EventPreToolUse, EventPostToolUse, EventPreCompact, EventPostCompact, EventStop:
		return true
	default:
		return false
	}
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
