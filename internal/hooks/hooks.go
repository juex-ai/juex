// Package hooks implements trusted command hooks for runtime lifecycle events.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	DefaultTimeoutSeconds = 10
	MaxTimeoutSeconds     = 300
	DefaultMaxOutputBytes = 64 * 1024
)

type EventName string

const (
	EventSessionStart     EventName = "SessionStart"
	EventUserPromptSubmit EventName = "UserPromptSubmit"
	EventPreToolUse       EventName = "PreToolUse"
	EventPostToolUse      EventName = "PostToolUse"
	EventPreCompact       EventName = "PreCompact"
	EventPostCompact      EventName = "PostCompact"
	EventStop             EventName = "Stop"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

type Config struct {
	Commands []CommandHook `json:"commands" yaml:"commands"`
}

type FileConfig struct {
	Trusted  bool          `yaml:"trusted"`
	Commands []CommandHook `yaml:"commands"`
}

type CommandHook struct {
	Name           string      `json:"name" yaml:"name"`
	Events         []EventName `json:"events" yaml:"events"`
	Tools          []string    `json:"tools,omitempty" yaml:"tools"`
	Command        []string    `json:"command" yaml:"command"`
	TimeoutSeconds int         `json:"timeout_seconds,omitempty" yaml:"timeout_seconds"`
	MaxOutputBytes int         `json:"max_output_bytes,omitempty" yaml:"max_output_bytes"`
	Source         string      `json:"source,omitempty" yaml:"-"`
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
	EventName        EventName      `json:"event_name"`
	SessionID        string         `json:"session_id,omitempty"`
	TurnID           string         `json:"turn_id,omitempty"`
	CWD              string         `json:"cwd,omitempty"`
	WorkspaceRoots   []string       `json:"workspace_roots,omitempty"`
	PermissionMode   string         `json:"permission_mode,omitempty"`
	SandboxMode      string         `json:"sandbox_mode,omitempty"`
	ConversationPath string         `json:"conversation_path,omitempty"`
	EventsPath       string         `json:"events_path,omitempty"`
	ToolName         string         `json:"tool_name,omitempty"`
	ToolInput        map[string]any `json:"tool_input,omitempty"`
	ToolResult       string         `json:"tool_result,omitempty"`
	UserInput        string         `json:"user_input,omitempty"`
	CompactReason    string         `json:"compact_reason,omitempty"`
	CompactAuto      bool           `json:"compact_auto,omitempty"`
	Observer         Observer       `json:"-"`
}

type Output struct {
	Decision          Decision `json:"decision,omitempty"`
	AdditionalContext string   `json:"additional_context,omitempty"`
	BlockStop         bool     `json:"block_stop,omitempty"`
	ContinuePrompt    string   `json:"continue_prompt,omitempty"`
}

type Result struct {
	Hook      CommandHook
	EventName EventName
	ToolName  string
	Output    Output
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
	hooks []CommandHook
}

func NewRunner(cfg Config) (*Runner, error) {
	hooks := append([]CommandHook(nil), cfg.Commands...)
	for i := range hooks {
		if err := validateHook(hooks[i]); err != nil {
			return nil, err
		}
	}
	return &Runner{hooks: hooks}, nil
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
		result, err := runCommandHook(ctx, hook, req)
		results = append(results, result)
		if err != nil {
			if req.Observer != nil {
				req.Observer.HookErrored(result, err)
			}
			return results, err
		}
		if req.Observer != nil {
			req.Observer.HookCompleted(result)
		}
	}
	return results, nil
}

func ParseOutput(data []byte) (Output, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Output{}, nil
	}
	var out Output
	if err := json.Unmarshal(data, &out); err != nil {
		return Output{}, fmt.Errorf("hooks: parse output: %w", err)
	}
	switch out.Decision {
	case "", DecisionAllow, DecisionDeny:
		return out, nil
	default:
		return Output{}, fmt.Errorf("hooks: invalid decision %q", out.Decision)
	}
}

func ResolveFileConfig(fc FileConfig, source string, requireTrust bool) (Config, error) {
	if len(fc.Commands) == 0 {
		return Config{}, nil
	}
	if requireTrust && !fc.Trusted {
		return Config{}, fmt.Errorf("hooks: project command hooks require hooks.trusted: true")
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

func runCommandHook(parent context.Context, hook CommandHook, req Request) (Result, error) {
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
	cmd := exec.CommandContext(ctx, hook.Command[0], hook.Command[1:]...)
	if req.CWD != "" {
		cmd.Dir = req.CWD
	}
	cmd.Env = os.Environ()
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
		return result, fmt.Errorf("hooks: %s failed: %w%s", hook.Name, err, stderrSuffix(result.Stderr))
	}
	out, err := ParseOutput([]byte(result.Stdout))
	if err != nil {
		return result, fmt.Errorf("hooks: %s: %w", hook.Name, err)
	}
	result.Output = out
	return result, nil
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
	case EventSessionStart, EventUserPromptSubmit, EventPreToolUse, EventPostToolUse, EventPreCompact, EventPostCompact, EventStop:
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
