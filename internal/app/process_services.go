package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/mcp"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/modelhealth"
)

// Options configures a Server. Provider is optional; if unset, each Thread
// resolves a provider profile from config and constructs a provider in app.
func (s *ProcessServices) RuntimeResolution() (AgentRuntimeResolution, error) {
	s.agentRuntimeOnce.Do(func() {
		s.agentRuntime, s.agentRuntimeErr = ResolveAgentRuntime(s.opts.Config)
	})
	return s.agentRuntime, s.agentRuntimeErr
}

func (s *ProcessServices) EnsureMCPStarted(ctx context.Context) (err error) {
	if err := ValidateModuleConfig(s.opts.Config); err != nil {
		return err
	}
	if !s.opts.Config.ModuleEnabled(string(mcp.ModuleID)) {
		return nil
	}
	s.mcpMu.Lock()
	if s.mcpStarted {
		starting := s.mcpStarting
		s.mcpMu.Unlock()
		if starting == nil {
			return nil
		}
		select {
		case <-starting:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mcpMu.Lock()
		defer s.mcpMu.Unlock()
		return s.mcpStartErr
	}
	s.mcpStarted = true
	s.mcpStartErr = nil
	starting := make(chan struct{})
	s.mcpStarting = starting
	s.mcpMu.Unlock()
	startupFinished := false
	finishStartup := func(startErr error) {
		s.mcpMu.Lock()
		if startErr != nil {
			s.mcpStarted = false
		}
		s.mcpStartErr = startErr
		s.mcpStarting = nil
		s.mcpMu.Unlock()
		close(starting)
	}
	defer func() {
		if !startupFinished {
			finishStartup(err)
		}
	}()

	agentRuntime, err := s.RuntimeResolution()
	if err != nil {
		return err
	}
	mcpConfigs, err := s.loadMCPConfigs(agentRuntime)
	if err != nil {
		return err
	}
	var ready atomic.Bool
	var queuedMu sync.Mutex
	var queued []mcp.Notification
	handleNotification := func(n mcp.Notification) {
		if !ready.Load() {
			queuedMu.Lock()
			queued = append(queued, n)
			queuedMu.Unlock()
			return
		}
		if err := s.deliverMCPNotification(context.Background(), n); err != nil {
			s.logVerbose("juex listen: MCP notification dropped: %v", err)
		}
	}
	mgr, err := mcp.NewManagerLayeredSoft(ctx, mcpConfigs, mcp.ConnectOptions{
		OnNotification:      handleNotification,
		EnableClaudeChannel: true,
		Environment:         agentRuntime.Environment(),
	})
	if err != nil {
		s.recordMCPError(err)
		s.logVerbose("juex listen: MCP startup failed: %v", err)
		return nil
	}
	s.setMCPErrors(mgr.StartupErrors())

	s.mcpMu.Lock()
	if s.closed {
		s.mcpMu.Unlock()
		if err := mgr.Close(); err != nil {
			s.logVerbose("juex listen: MCP shutdown failed: %v", err)
		}
		return nil
	}
	s.mcpManager = mgr
	s.mcpMu.Unlock()
	ready.Store(true)
	finishStartup(nil)
	startupFinished = true
	queuedMu.Lock()
	pending := append([]mcp.Notification(nil), queued...)
	queued = nil
	queuedMu.Unlock()
	for _, n := range pending {
		handleNotification(n)
	}
	return nil
}

func (s *ProcessServices) mcpManagerSnapshot() *mcp.Manager {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	return s.mcpManager
}

func (s *ProcessServices) mcpToolDescriptors() map[string][]mcp.ToolDescriptor {
	mgr := s.mcpManagerSnapshot()
	if mgr == nil {
		return map[string][]mcp.ToolDescriptor{}
	}
	return mgr.ToolDescriptors()
}

func (s *ProcessServices) mcpConnectionSpecs() map[string]mcp.RuntimeConnectionSpec {
	s.mcpMu.Lock()
	started := s.mcpStarted
	mgr := s.mcpManager
	s.mcpMu.Unlock()
	if !started {
		return nil
	}
	if mgr == nil {
		return map[string]mcp.RuntimeConnectionSpec{}
	}
	return mgr.RuntimeConnectionSpecs()
}

func (s *ProcessServices) Close() {
	s.mcpMu.Lock()
	s.closed = true
	mgr := s.mcpManager
	s.mcpManager = nil
	s.mcpMu.Unlock()
	if mgr != nil {
		_ = mgr.Close()
	}
}

func (s *ProcessServices) recordMCPError(err error) {
	name, ok := mcp.ErrorServerName(err)
	if !ok {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeMCPErr == nil {
		s.runtimeMCPErr = map[string]string{}
	}
	s.runtimeMCPErr[name] = s.RedactRuntimeText(err.Error())
}

func (s *ProcessServices) setMCPErrors(errors map[string]string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtimeMCPErr = map[string]string{}
	for name, msg := range errors {
		if msg != "" {
			s.runtimeMCPErr[name] = s.RedactRuntimeText(msg)
		}
	}
}

func (s *ProcessServices) RedactRuntimeText(message string) string {
	runtime, err := s.RuntimeResolution()
	if err != nil {
		return message
	}
	redacted, _ := runtime.Environment().RedactConfiguredValues([]byte(message))
	return string(redacted)
}

func (s *ProcessServices) mcpErrors() map[string]string {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	out := make(map[string]string, len(s.runtimeMCPErr))
	for name, msg := range s.runtimeMCPErr {
		out[name] = msg
	}
	return out
}

func (s *ProcessServices) stderr() io.Writer {
	if s.opts.Stderr != nil {
		return s.opts.Stderr
	}
	return os.Stderr
}

func (s *ProcessServices) logVerbose(format string, args ...any) {
	if !s.opts.Verbose {
		return
	}
	message := fmt.Sprintf(format, args...)
	fmt.Fprintln(s.stderr(), s.RedactRuntimeText(message))
}

func (s *ProcessServices) loadMCPConfigs(runtime AgentRuntimeResolution) ([]mcp.Config, error) {
	return LoadMCPConfigs(runtime, s.absoluteWorkDir())
}

func (s *ProcessServices) absoluteWorkDir() string {
	workDir := s.opts.Config.WorkDir
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		workDir = cwd
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return workDir
	}
	return abs
}

// ProcessServices owns the shared application resources of an Agent process.
// Transports retain their Thread bindings and execute WithMain under a work lease.
type ProcessServices struct {
	opts             ProcessServicesOptions
	modelHealth      *modelhealth.ModelHealth
	agentRuntimeOnce sync.Once
	agentRuntime     AgentRuntimeResolution
	agentRuntimeErr  error
	runtimeMu        sync.Mutex
	runtimeMCPErr    map[string]string
	mcpMu            sync.Mutex
	mcpStarted       bool
	mcpStarting      chan struct{}
	mcpStartErr      error
	mcpManager       *mcp.Manager
	closed           bool
}

type ProcessServicesOptions struct {
	Config         config.Config
	Provider       llm.Provider
	Verbose, Debug bool
	LogLevel       string
	Stderr         io.Writer
	WithMain       func(context.Context, func(context.Context, *App) error) error
}

func NewProcessServices(opts ProcessServicesOptions) *ProcessServices {
	return &ProcessServices{opts: opts, modelHealth: modelhealth.NewModelHealth(modelhealth.ModelHealthOptions{}), runtimeMCPErr: map[string]string{}}
}

// NewThread uses resources prepared by EnsureMCPStarted. The caller must finish
// that startup outside its Thread-creation lock because notifications reenter Main.
func (s *ProcessServices) NewThread(id string) (*App, error) {
	resolution, err := s.RuntimeResolution()
	if err != nil {
		return nil, err
	}
	a, err := New(Options{Config: s.opts.Config, Provider: s.opts.Provider, ModelHealth: s.modelHealth,
		Verbose: s.opts.Verbose, Debug: s.opts.Debug, LogLevel: s.opts.LogLevel, Stderr: s.stderr(), WorkDir: s.opts.Config.WorkDir,
		MCPManager: s.mcpManagerSnapshot(), DisableMCP: true, ThreadID: id, AgentRuntime: &resolution})
	if err != nil {
		s.recordMCPError(err)
	}
	return a, err
}

func (s *ProcessServices) RuntimeStatus(active *agent.Agent) (RuntimeStatus, error) {
	resolution, err := s.RuntimeResolution()
	if err != nil {
		return RuntimeStatus{}, err
	}
	var status RuntimeStatus
	err = ReadRuntimeModuleSnapshot(active, func(snapshot RuntimeModuleSnapshot) error {
		var err error
		status, err = NewRuntimeCatalogService(s.opts.Config).Snapshot(RuntimeStatusOptions{ActiveModules: &snapshot,
			MCPToolDescriptors: s.mcpToolDescriptors(), MCPErrors: s.mcpErrors(), MCPConnectionSpecs: s.mcpConnectionSpecs(), AgentRuntime: &resolution})
		return err
	})
	return status, err
}

func (s *ProcessServices) deliverMCPNotification(ctx context.Context, notification mcp.Notification) error {
	return s.opts.WithMain(ctx, func(workCtx context.Context, main *App) error {
		_, err := main.DeliverObservation(workCtx, main.ObservationFromMCPNotification(notification))
		return err
	})
}
