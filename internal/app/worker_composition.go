package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

type workerThreadChildOptions struct {
	Context           context.Context
	Config            config.Config
	ThreadID          string
	Alias             string
	Model             string
	UseParentProvider bool
}

type workerThreadFactory func(workerThreadChildOptions) (*App, error)

func (parent *App) newWorkerChild(child workerThreadChildOptions) (*App, error) {
	if parent == nil {
		return nil, agent.ErrWorkerThreadManagerClosed
	}
	opts := Options{
		Config:               child.Config,
		ModelHealth:          parent.Engine.ModelHealth,
		SummaryProvider:      parent.Engine.SummaryProvider,
		SummaryProvenance:    parent.Engine.SummaryProvenance,
		SummaryContextWindow: parent.Engine.SummaryContextWindow,
		Verbose:              false,
		Debug:                parent.debug,
		LogLevel:             parent.logLevel,
		Stderr:               parent.stderr,
		WorkDir:              child.Config.WorkDir,
		MCPManager:           parent.mcpManager,
		DisableMCP:           true,
		ThreadID:             child.ThreadID,
		Alias:                child.Alias,
		AgentRuntime:         &parent.agentRuntime,
		disableObservables:   true,
		startupContext:       child.Context,
	}
	if child.UseParentProvider {
		opts.Provider = parent.Engine.Provider
		opts.ModelCandidates = append([]runtime.ModelCandidate(nil), parent.Engine.ModelCandidates...)
	}
	return New(opts)
}

func (a *App) prepareWorkerChild(model string) (agent.PreparedChild, error) {
	model = strings.TrimSpace(model)
	useParentProvider := model == ""
	cfg := a.cfg
	if model != "" {
		if err := cfg.ApplyModelOverride(model); err != nil {
			return agent.PreparedChild{}, fmt.Errorf("worker thread model: %w", err)
		}
	} else {
		model = config.ModelRef{ProviderID: cfg.ProviderID, ModelID: cfg.Model}.String()
	}
	factory := a.workerFactory
	if factory == nil {
		factory = a.newWorkerChild
	}
	return agent.PreparedChild{Model: model, Open: func(request agent.ChildRequest) (*agent.Agent, error) {
		child, err := factory(workerThreadChildOptions{Context: request.Context, Config: cfg, ThreadID: request.ThreadID, Alias: request.Alias, Model: model, UseParentProvider: useParentProvider})
		if child == nil {
			return nil, err
		}
		return child.Agent, err
	}}, nil
}
