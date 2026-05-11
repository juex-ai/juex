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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	// ResumeDir, if non-empty, is the absolute path of an existing
	// session directory to load instead of creating a new one. The
	// session ID and on-disk files are reused; new messages append.
	ResumeDir string
	Alias     string
}

type App struct {
	Engine  *runtime.Engine
	Bus     *events.Bus
	Session *session.Session
	cleanup []func() error
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

	var mcpClients []*mcp.Client
	var mcpConfigs []mcp.Config
	for _, path := range cfg.MCPConfigPaths() {
		mcpCfg, err := mcp.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		if len(mcpCfg.MCPServers) > 0 {
			mcpConfigs = append(mcpConfigs, mcpCfg)
		}
	}
	if len(mcpConfigs) > 0 {
		clients, err := mcp.RegisterAllLayered(context.Background(), mcpConfigs, reg)
		mcpClients = append(mcpClients, clients...)
		if err != nil {
			closeAll(mcpClients)
			return nil, err
		}
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
		})
	}
	if err != nil {
		closeAll(mcpClients)
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
		Provider: provider,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt:   pb,
	}

	a := &App{Engine: eng, Bus: bus, Session: sess}
	a.cleanup = append(a.cleanup, sess.Close)
	for _, c := range mcpClients {
		c := c
		a.cleanup = append(a.cleanup, c.Close)
	}
	return a, nil
}

// Run drives a single turn synchronously.
func (a *App) Run(ctx context.Context, prompt string) (string, error) {
	return a.Engine.Turn(ctx, prompt)
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
		text, err := a.Engine.Turn(ctx, line)
		if err != nil {
			if _, writeErr := fmt.Fprintln(out, "error:", err); writeErr != nil {
				return writeErr
			}
			continue
		}
		if _, err := fmt.Fprintln(out, text); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Close releases session file handles and MCP subprocesses.
func (a *App) Close() error {
	var firstErr error
	for _, fn := range a.cleanup {
		if err := fn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func closeAll(clients []*mcp.Client) {
	for _, c := range clients {
		c.Close()
	}
}
