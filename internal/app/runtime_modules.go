package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/agentsmd"
	chunkmodule "github.com/juex-ai/juex/internal/features/chunkedwrite"
	goalmodule "github.com/juex-ai/juex/internal/features/goal"
	"github.com/juex-ai/juex/internal/features/hooks"
	"github.com/juex-ai/juex/internal/features/memory"
	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	"github.com/juex-ai/juex/internal/features/operatingcontext"
	"github.com/juex-ai/juex/internal/features/scratchpad"
	shelltools "github.com/juex-ai/juex/internal/features/shell"
	"github.com/juex-ai/juex/internal/features/skills"
	skillsmodule "github.com/juex-ai/juex/internal/features/skills/module"
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	juexruntime "github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/runtime/workmem"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/internal/modules/builtintools"
	"github.com/juex-ai/juex/internal/tools"
)

type runtimeModuleComposition struct {
	set            *runtimemodule.Set
	shell          *shelltools.Module
	skills         *skillsmodule.Module
	constructed    *constructedRuntimeModules
	runtimeContext runtimemodule.RuntimeContext
	specs          []runtimemodule.RuntimeFactorySpec
}

type constructedRuntimeModules struct {
	shell  *shelltools.Module
	skills *skillsmodule.Module
}

type threadModuleOptions struct {
	hookRunner               hooks.PolicyRunner
	hookBaseRequest          hooks.Request
	goalState                *workmem.GoalStateStore
	notes                    *workmem.NotesStore
	goalContinuation         bool
	goalContinuationDeferrer goalmodule.ContinuationDeferrer
}

func prepareRuntimeModules(
	_ context.Context,
	cfg config.Config,
	resourceGraph RuntimeResourceGraph,
	runtimePaths config.RuntimePaths,
	runtimeEnvironment environment.Snapshot,
	sandboxRunner sandbox.Runner,
	toolTimeoutSeconds int,
) (runtimeModuleComposition, error) {
	runtimeContext := runtimemodule.RuntimeContext{
		ID:            cfg.AgentAddress.ID(),
		WorkDir:       runtimePaths.WorkDir,
		AgentStateDir: runtimePaths.StateDir,
		MediaDir:      runtimePaths.MediaDir,
	}
	constructed := &constructedRuntimeModules{}
	composition := runtimeModuleComposition{runtimeContext: runtimeContext, constructed: constructed}
	toolOptions := tools.BuiltinOptions{
		WorkDir:            runtimePaths.WorkDir,
		Environment:        runtimeEnvironment,
		Shell:              toolsShellProfile(cfg.Shell),
		Sandbox:            cfg.SandboxPolicy(),
		SandboxRunner:      sandboxRunner,
		ToolTimeoutSeconds: toolTimeoutSeconds,
		AgentStateDir:      runtimePaths.StateDir,
		MediaDir:           runtimePaths.MediaDir,
	}
	composition.specs = []runtimemodule.RuntimeFactorySpec{
		{
			ID:      memory.ModuleID,
			Enabled: cfg.ModuleEnabled(modulecatalog.Memory),
			New: func(_ context.Context, ctx runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				if ctx.AgentStateDir == "" {
					return nil, fmt.Errorf("memory module requires an Agent state directory")
				}
				agentDir, err := filepath.Abs(ctx.AgentStateDir)
				if err != nil {
					return nil, fmt.Errorf("resolve memory Agent state directory: %w", err)
				}
				return memory.New(agentDir), nil
			},
		},
		{
			ID:      modulecatalog.BasicFileTools,
			Enabled: cfg.ModuleEnabled(modulecatalog.BasicFileTools),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return builtintools.NewBasicFiles(toolOptions), nil
			},
		},
		{
			ID:      modulecatalog.ApplyPatch,
			Enabled: cfg.ModuleEnabled(modulecatalog.ApplyPatch),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return builtintools.NewApplyPatch(toolOptions), nil
			},
		},
		{
			ID:      modulecatalog.FileSearch,
			Enabled: cfg.ModuleEnabled(modulecatalog.FileSearch),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return builtintools.NewFileSearch(toolOptions), nil
			},
		},
		{
			ID:      shelltools.ModuleID,
			Enabled: cfg.ModuleEnabled(modulecatalog.Shell),
			New: func(ctx context.Context, _ runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				constructed.shell = shelltools.New(ctx, toolOptions)
				return constructed.shell, nil
			},
		},
		{
			ID:      agentsmd.ModuleID,
			Enabled: cfg.ModuleEnabled(string(agentsmd.ModuleID)),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return &agentsmd.Module{
					GlobalAgentsMDPath: cfg.GlobalAgentsMDPath(),
					AgentsMDDirs:       cfg.AgentsMDDirs(),
				}, nil
			},
		},
		{
			ID:      skillsmodule.ModuleID,
			Enabled: cfg.ModuleEnabled(string(skillsmodule.ModuleID)),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				mod, err := skillsmodule.New(skillsmodule.Options{
					Dirs:          resourceGraph.SkillDirs(),
					LoaderOptions: skillLoaderOptions(cfg),
					WorkDir:       runtimePaths.WorkDir,
					Sandbox:       cfg.SandboxPolicy(),
				})
				if err != nil {
					return nil, err
				}
				constructed.skills = mod
				return mod, nil
			},
		},
	}
	return composition, nil
}

// ValidateModuleConfig validates declarations without constructing resources.
func ValidateModuleConfig(cfg config.Config) error {
	return cfg.ValidateModules()
}

func (c *runtimeModuleComposition) sealAndStart(ctx context.Context, extra ...runtimemodule.RuntimeFactorySpec) error {
	specs := append(append([]runtimemodule.RuntimeFactorySpec(nil), c.specs...), extra...)
	set, err := runtimemodule.BuildAndStartRuntimeSet(ctx, specs, c.runtimeContext, runtimemodule.ToolContext{Runtime: c.runtimeContext})
	if err != nil {
		return err
	}
	c.set = set
	if c.constructed != nil {
		c.shell = c.constructed.shell
		c.skills = c.constructed.skills
	}
	return nil
}

func buildThreadModules(
	ctx context.Context,
	cfg config.Config,
	specs []runtimemodule.ThreadFactorySpec,
	runtimeContext runtimemodule.RuntimeContext,
	threadState *thread.Thread,
	engine *juexruntime.Engine,
	workDir string,
	opts threadModuleOptions,
) (*runtimemodule.Set, error) {
	var set *runtimemodule.Set
	specs = threadFactorySpecs(cfg, specs, threadState, engine, workDir, opts, func() []byte { return juexruntime.HookGoalStateFromModules(set) })
	threadContext := threadModuleContext(threadState)
	var err error
	set, err = runtimemodule.BuildAndStartThreadSet(ctx, specs, threadContext, runtimemodule.ToolContext{Runtime: runtimeContext, Thread: &threadContext})
	return set, err
}

func threadFactorySpecs(cfg config.Config, extra []runtimemodule.ThreadFactorySpec, threadState *thread.Thread, engine *juexruntime.Engine, workDir string, opts threadModuleOptions, goalState func() []byte) []runtimemodule.ThreadFactorySpec {
	eventSink := func(event events.Event) error {
		if engine == nil || engine.Bus == nil {
			return nil
		}
		return engine.Bus.Emit(event)
	}
	currentTurnID := func() string {
		if engine == nil {
			return ""
		}
		return engine.PendingInputStatus().TurnID
	}
	builtinSpecs := []runtimemodule.ThreadFactorySpec{
		{
			ID:      chunkmodule.ModuleID,
			Enabled: cfg.ModuleEnabled(modulecatalog.ChunkedWrite),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				paths := cfg.RuntimePaths()
				return chunkmodule.New(tools.BuiltinOptions{WorkDir: workDir, Sandbox: cfg.SandboxPolicy(), AgentStateDir: paths.StateDir, MediaDir: paths.MediaDir}), nil
			},
		},
		{
			ID:      juexruntime.ContextControlModuleID,
			Enabled: cfg.ModuleEnabled(string(juexruntime.ContextControlModuleID)),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return juexruntime.NewContextControlModule(engine), nil
			},
		},
		{
			ID:      operatingcontext.ModuleID,
			Enabled: cfg.ModuleEnabled(modulecatalog.OperatingContext),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return &operatingcontext.Module{WorkDir: workDir}, nil
			},
		},
		{
			ID:         scratchpad.ModuleID,
			Inspection: scratchpad.Inspection(),
			Enabled:    cfg.ModuleEnabled(modulecatalog.Scratchpad),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return &scratchpad.Module{WorkDir: workDir}, nil
			},
		},
		{
			ID:            goalmodule.ModuleID,
			Inspection:    goalmodule.Inspection(),
			OwnsResources: true,
			Enabled:       cfg.ModuleEnabled(string(goalmodule.ModuleID)),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				goalState := opts.goalState
				if goalState == nil {
					goalState = goalStateStore(threadState)
				}
				return goalmodule.NewWithOptions(goalState, goalmodule.Options{
					EnableContinuation:   opts.goalContinuation,
					ContinuationDeferrer: opts.goalContinuationDeferrer,
					EventSink:            eventSink,
					CurrentTurnID:        currentTurnID,
				}), nil
			},
		},
		{
			ID:            notesmodule.ModuleID,
			Inspection:    notesmodule.Inspection(),
			OwnsResources: true,
			Enabled:       cfg.ModuleEnabled(string(notesmodule.ModuleID)),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				notes := opts.notes
				if notes == nil {
					notes = notesStore(threadState)
				}
				return notesmodule.NewWithOptions(notes, notesmodule.Options{
					EventSink:     eventSink,
					CurrentTurnID: currentTurnID,
				}), nil
			},
		},
	}
	if opts.hookRunner != nil && cfg.ModuleEnabled(string(hooks.ModuleID)) {
		builtinSpecs = append(builtinSpecs, runtimemodule.ThreadFactorySpec{
			ID:      hooks.ModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				base := opts.hookBaseRequest
				base.ThreadID = threadState.ID
				return hooks.NewModule(opts.hookRunner, hooks.ModuleOptions{
					BaseRequest:           base,
					GenerationJournalPath: threadState.CurrentGenerationJournalPath,
					GoalState:             goalState,
				}), nil
			},
		})
	}
	return append(builtinSpecs, extra...)
}

func validateThreadModuleContext(
	ctx context.Context,
	runtimeSet *runtimemodule.Set,
	threadSet *runtimemodule.Set,
	runtimeContext runtimemodule.RuntimeContext,
	threadState *thread.Thread,
) error {
	threadContext := threadModuleContext(threadState)
	_, err := runtimemodule.CollectContext(ctx, runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Runtime: runtimeContext,
		Thread:  &threadContext,
	}, runtimeSet, threadSet)
	return err
}

func threadModuleContext(threadState *thread.Thread) runtimemodule.ThreadContext {
	if threadState == nil {
		return runtimemodule.ThreadContext{}
	}
	return runtimemodule.ThreadContext{
		ID:  threadState.ID,
		Dir: threadState.Dir,
	}
}

func skillLoaderOptions(cfg config.Config) skills.LoaderOptions {
	policy := cfg.SkillPolicy()
	return skills.LoaderOptions{Policy: skills.Policy{
		Include:           policy.Include,
		Exclude:           policy.Exclude,
		PromptBudgetChars: policy.PromptBudgetChars,
	}}
}
