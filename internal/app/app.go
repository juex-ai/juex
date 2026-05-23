// Package app wires the runtime: config -> provider -> registry -> tools ->
// MCP -> skills -> memory -> session -> prompt -> engine.
//
// CLI layers should depend only on this package; tests can substitute the
// Provider via Options.Provider so the runtime is exercised without hitting
// the network.
package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

// Options bundles the inputs to New.
type Options struct {
	Config   config.Config
	Provider llm.Provider // optional; if nil, derived from Config
	Verbose  bool
	Stderr   io.Writer
	WorkDir  string // if set, overrides Config.WorkDir
	// MCPManager, when set, provides process-scoped MCP clients owned by
	// the caller. App registers proxy tools into its per-session registry
	// but does not close the manager.
	MCPManager *mcp.Manager
	// DisableMCP skips loading MCP configs. Used by serve when MCP startup
	// failed at process scope but sessions should still be usable.
	DisableMCP bool
	// SuppressMCPWarnings keeps optional MCP startup diagnostics out of stderr.
	// Callers that expose structured diagnostics, such as dry-run JSON, set this.
	SuppressMCPWarnings bool
	// ResumeDir, if non-empty, is the absolute path of an existing
	// session directory to load instead of creating a new one. The
	// session ID and on-disk files are reused; new messages append.
	ResumeDir string
	Alias     string
	// LazySession delays creating the on-disk session directory until the
	// first message or event is appended. Used by the web UI so abandoned
	// empty chats do not leave local files behind.
	LazySession bool
}

type App struct {
	Engine  *runtime.Engine
	Bus     *events.Bus
	Session *session.Session
	cleanup []func() error
	ctx     context.Context
	cancel  context.CancelFunc
	cfg     config.Config
	skills  []skills.Skill
	mcp     MCPStatus
}

type MCPStatus struct {
	Configured int               `json:"configured"`
	Connected  int               `json:"connected"`
	Errors     int               `json:"errors"`
	Servers    []MCPServerStatus `json:"servers"`
}

type MCPServerStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"tool_count"`
	Error     string `json:"error,omitempty"`
}

// New wires every subsystem and returns a ready-to-use App.
// The caller must Close() to flush jsonl and stop MCP subprocesses.
func New(opts Options) (*App, error) {
	cfg := opts.Config
	if opts.WorkDir != "" {
		cfg.WorkDir = opts.WorkDir
	}
	if cfg.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("app: workdir: %w", err)
		}
		cfg.WorkDir = wd
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	provider := opts.Provider
	if provider == nil {
		p, err := cfg.NewProvider()
		if err != nil {
			return nil, err
		}
		provider = p
	}

	bus := events.NewBus()
	if opts.Verbose {
		vp := newVerbosePrinter(stderr)
		bus.Subscribe("*", vp.handle)
	}

	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, cfg.WorkDir)

	skillLoader := skills.NewLoader(cfg.SkillDirs()...)
	if err := skillLoader.Load(); err != nil {
		return nil, err
	}
	// Skills are surfaced via the system prompt's "Available Skills"
	// section (each entry includes its absolute path); the model loads a
	// skill body with the standard `read` builtin. No dedicated tool.

	memStore := memory.NewStore(cfg.MemoryDir())
	if err := memStore.RegisterTools(reg); err != nil {
		return nil, err
	}

	var mcpConfigs []mcp.Config
	var mergedMCP mcp.Config
	if !opts.DisableMCP && opts.MCPManager == nil {
		for _, path := range cfg.MCPConfigPaths() {
			mcpCfg, err := mcp.LoadConfig(path)
			if err != nil {
				return nil, err
			}
			if len(mcpCfg.MCPServers) > 0 {
				mcpConfigs = append(mcpConfigs, mcpCfg)
			}
		}
		mergedMCP = mcp.MergeConfigs(mcpConfigs)
	}

	var sess *session.Session
	var err error
	if opts.ResumeDir != "" {
		sess, err = session.LoadWithOptions(opts.ResumeDir, session.Options{
			Alias:       opts.Alias,
			HistoryPath: cfg.HistoryPath(),
		})
	} else {
		sess, err = session.NewWithOptions(cfg.SessionsDir(), session.Options{
			Alias:       opts.Alias,
			HistoryPath: cfg.HistoryPath(),
			Lazy:        opts.LazySession,
		})
	}
	if err != nil {
		return nil, err
	}
	sess.SubscribeBus(bus)

	var globalAgents string
	if cfg.HomeAgentsDir != "" {
		globalAgents = filepath.Join(cfg.HomeAgentsDir, "AGENTS.md")
	}
	pb := &prompt.Builder{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       cfg.AgentsMDDirs(),
		Memory:             memStore,
		Skills:             skillLoader,
	}

	eng := &runtime.Engine{
		Provider:      provider,
		Tools:         reg,
		Bus:           bus,
		Session:       sess,
		Prompt:        pb,
		ContextWindow: cfg.ContextWindow,
		Compaction:    cfg.Compaction,
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	a := &App{
		Engine:  eng,
		Bus:     bus,
		Session: sess,
		ctx:     appCtx,
		cancel:  appCancel,
		cfg:     cfg,
		skills:  skillLoader.All(),
	}
	a.mcp = buildMCPStatus(mergedMCP.MCPServers, nil, nil)
	a.cleanup = append(a.cleanup, sess.Close)
	if opts.MCPManager != nil {
		if err := opts.MCPManager.RegisterTools(reg); err != nil {
			sess.Close()
			return nil, err
		}
		a.mcp = buildMCPStatus(nil, opts.MCPManager.ToolCounts(), opts.MCPManager.StartupErrors())
	} else if len(mcpConfigs) > 0 {
		mgr, err := mcp.NewManagerLayeredSoft(context.Background(), mcpConfigs, mcp.ConnectOptions{
			OnNotification: func(n mcp.Notification) {
				_ = a.handleMCPNotification(a.ctx, n)
			},
		})
		if err != nil {
			sess.Close()
			return nil, err
		}
		startupErrors := mgr.StartupErrors()
		if !opts.SuppressMCPWarnings {
			writeMCPStartupWarnings(stderr, startupErrors)
		}
		if err := mgr.RegisterTools(reg); err != nil {
			if closeErr := mgr.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			sess.Close()
			return nil, err
		}
		a.mcp = buildMCPStatus(mergedMCP.MCPServers, mgr.ToolCounts(), startupErrors)
		a.cleanup = append(a.cleanup, mgr.Close)
	}
	return a, nil
}

// Run drives a single turn synchronously.
func (a *App) Run(ctx context.Context, prompt string) (string, error) {
	if result, handled, err := a.ExecuteSlashCommand(ctx, prompt); handled || err != nil {
		if err != nil {
			return "", err
		}
		return result.Text, nil
	}
	return a.Engine.Turn(ctx, prompt)
}

func (a *App) Compact(ctx context.Context, reason string, auto bool) (runtime.CompactionResult, error) {
	if a == nil || a.Engine == nil {
		return runtime.CompactionResult{}, fmt.Errorf("app: nil engine")
	}
	sections := a.Engine.Prompt.Sections()
	systemPrompt := prompt.JoinSections(sections)
	return a.Engine.Compact(ctx, "session-compact", systemPrompt, reason, auto)
}

func (a *App) handleMCPNotification(ctx context.Context, n mcp.Notification) error {
	if a == nil || a.Engine == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	eventType := n.EventType
	if eventType == "" {
		eventType = "notification"
	}
	msg := llm.TextMessage(llm.RoleUser, fmt.Sprintf("%s:%s:%s", n.ServerName, eventType, n.Content))
	msg.Kind = llm.MessageKindMCPEvent
	if _, err := a.Engine.EnqueuePendingMessage(ctx, msg); err == nil {
		return nil
	} else if !errors.Is(err, runtime.ErrNoActiveTurn) {
		return err
	}
	_, err := a.Engine.TurnMessage(ctx, msg)
	return err
}

func (a *App) HandleMCPNotification(ctx context.Context, n mcp.Notification) error {
	return a.handleMCPNotification(ctx, n)
}

func (a *App) TokenUsage() llm.Usage {
	if a == nil || a.Session == nil {
		return llm.Usage{}
	}
	info := a.Session.Info(time.Now().UTC())
	return info.TokenUsage
}

func (a *App) MCPStatus() MCPStatus {
	if a == nil {
		return MCPStatus{}
	}
	status := a.mcp
	status.Servers = append([]MCPServerStatus(nil), status.Servers...)
	return status
}

func buildMCPStatus(configured map[string]mcp.ServerSpec, toolCounts map[string]int, startupErrors map[string]string) MCPStatus {
	names := map[string]struct{}{}
	for name := range configured {
		names[name] = struct{}{}
	}
	for name := range toolCounts {
		names[name] = struct{}{}
	}
	for name := range startupErrors {
		names[name] = struct{}{}
	}

	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	configuredCount := len(configured)
	if configuredCount == 0 && len(names) > 0 {
		configuredCount = len(names)
	}
	status := MCPStatus{
		Configured: configuredCount,
		Servers:    make([]MCPServerStatus, 0, len(ordered)),
	}
	for _, name := range ordered {
		count, connected := toolCounts[name]
		errText := startupErrors[name]
		server := MCPServerStatus{
			Name:      name,
			Status:    "not_started",
			Connected: connected,
			ToolCount: count,
			Error:     errText,
		}
		if server.Connected {
			server.Status = "connected"
			status.Connected++
		} else if errText != "" {
			server.Status = "error"
			status.Errors++
		}
		status.Servers = append(status.Servers, server)
	}
	return status
}

func writeMCPStartupWarnings(w io.Writer, startupErrors map[string]string) {
	if w == nil || len(startupErrors) == 0 {
		return
	}
	names := make([]string, 0, len(startupErrors))
	for name := range startupErrors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "juex: warning: optional MCP server %q is unavailable: %s\n", name, startupErrors[name])
	}
}

// REPL reads stdin lines, runs Turn for each non-empty line, prints the
// result on out. Returns when the reader closes.
func (a *App) REPL(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		text, err := a.Run(ctx, line)
		if err != nil {
			if _, writeErr := fmt.Fprintln(out, "error:", err); writeErr != nil {
				return writeErr
			}
			continue
		}
		if _, err := fmt.Fprintln(out, text); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, FormatTokenUsage(a.TokenUsage())); err != nil {
			return err
		}
	}
	return sc.Err()
}

func FormatTokenUsage(usage llm.Usage) string {
	return fmt.Sprintf("tokens: %d total (input %d, output %d)",
		usage.TotalTokens(), usage.InputTokens, usage.OutputTokens)
}

// Close releases session file handles and MCP subprocesses.
func (a *App) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	var firstErr error
	for _, fn := range a.cleanup {
		if err := fn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
