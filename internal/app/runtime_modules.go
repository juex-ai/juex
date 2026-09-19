package app

import (
	"context"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/agentsmd"
	"github.com/juex-ai/juex/internal/features/applypatch"
	chunkmodule "github.com/juex-ai/juex/internal/features/chunkedwrite"
	"github.com/juex-ai/juex/internal/features/contextcontrol"
	"github.com/juex-ai/juex/internal/features/filesearch"
	"github.com/juex-ai/juex/internal/features/filetools"
	"github.com/juex-ai/juex/internal/features/fleetmanagement"
	"github.com/juex-ai/juex/internal/features/hooks"
	"github.com/juex-ai/juex/internal/features/inputtracking"
	"github.com/juex-ai/juex/internal/features/memory"
	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	"github.com/juex-ai/juex/internal/features/operatingcontext"
	"github.com/juex-ai/juex/internal/features/scratchpad"
	shelltools "github.com/juex-ai/juex/internal/features/shell"
	"github.com/juex-ai/juex/internal/features/skills"
	tasksmodule "github.com/juex-ai/juex/internal/features/tasks"
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/fleetclient"
	"github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	juexruntime "github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type runtimeModuleComposition struct {
	set            *runtimemodule.Set
	shell          *shelltools.Module
	skills         *skills.Module
	constructed    *constructedRuntimeModules
	runtimeContext runtimemodule.RuntimeContext
	specs          []runtimemodule.RuntimeFactorySpec
}

type constructedRuntimeModules struct {
	shell  *shelltools.Module
	skills *skills.Module
}

type threadModuleOptions struct {
	hookRunner                hooks.PolicyRunner
	hookBaseRequest           hooks.Request
	tasksState                *tasksmodule.Store
	notes                     *notesmodule.NotesStore
	tasksContinuation         bool
	tasksContinuationDeferrer tasksmodule.ContinuationDeferrer
	memoryAssignment          *memoryclient.Assignment
}

func prepareRuntimeModules(
	_ context.Context,
	cfg config.Config,
	resourceGraph RuntimeResourceGraph,
	runtimePaths agentstate.RuntimePaths,
	runtimeEnvironment environment.Snapshot,
	sandboxRunner sandbox.Runner,
) (runtimeModuleComposition, error) {
	runtimeContext := runtimemodule.RuntimeContext{
		ID:            cfg.AgentAddress.ID(),
		WorkDir:       runtimePaths.WorkDir,
		AgentStateDir: runtimePaths.StateDir,
		MediaDir:      runtimePaths.MediaDir,
	}
	constructed := &constructedRuntimeModules{}
	composition := runtimeModuleComposition{runtimeContext: runtimeContext, constructed: constructed}
	filePolicy := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: cfg.SandboxPolicy(), WorkDir: runtimePaths.WorkDir, AgentStateDir: runtimePaths.StateDir, ReadOnlyPaths: []string{runtimePaths.MediaDir}})
	composition.specs = []runtimemodule.RuntimeFactorySpec{
		{
			ID:      filetools.ModuleID,
			Enabled: cfg.ModuleEnabled(filetools.ModuleID),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return filetools.New(filetools.Options{WorkDir: runtimePaths.WorkDir, MediaDir: runtimePaths.MediaDir, FilePolicy: filePolicy}), nil
			},
		},
		{
			ID:      applypatch.ModuleID,
			Enabled: cfg.ModuleEnabled(applypatch.ModuleID),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return applypatch.New(applypatch.Options{WorkDir: runtimePaths.WorkDir, FilePolicy: filePolicy}), nil
			},
		},
		{
			ID:      filesearch.ModuleID,
			Enabled: cfg.ModuleEnabled(filesearch.ModuleID),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return filesearch.New(filesearch.Options{WorkDir: runtimePaths.WorkDir, Environment: runtimeEnvironment, Sandbox: cfg.SandboxPolicy(), SandboxRunner: sandboxRunner, FilePolicy: filePolicy}), nil
			},
		},
		{
			ID:      shelltools.ModuleID,
			Enabled: cfg.ModuleEnabled(shelltools.ModuleID),
			New: func(ctx context.Context, _ runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				constructed.shell = shelltools.New(ctx, shelltools.Options{WorkDir: runtimePaths.WorkDir, Environment: runtimeEnvironment, Shell: toolsShellProfile(cfg.Shell), Sandbox: cfg.SandboxPolicy(), SandboxRunner: sandboxRunner, FilePolicy: filePolicy, MediaDir: runtimePaths.MediaDir})
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
			ID:      skills.ModuleID,
			Enabled: cfg.ModuleEnabled(string(skills.ModuleID)),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				mod, err := skills.New(skills.Options{
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
	specs = threadFactorySpecs(cfg, specs, threadState, engine, workDir, opts, func() []byte { return tasksmodule.HookStateFromModules(set) })
	threadContext := threadModuleContext(threadState)
	var err error
	set, err = runtimemodule.BuildAndStartThreadSet(ctx, specs, threadContext, runtimemodule.ToolContext{Runtime: runtimeContext, Thread: &threadContext})
	return set, err
}

func threadFactorySpecs(cfg config.Config, extra []runtimemodule.ThreadFactorySpec, threadState *thread.Thread, engine *juexruntime.Engine, workDir string, opts threadModuleOptions, tasksState func() []byte) []runtimemodule.ThreadFactorySpec {
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
	var notesOwner *notesmodule.Module
	var clearCompletedNotes func() error
	if cfg.ModuleEnabled(notesmodule.ModuleID) {
		clearCompletedNotes = func() error { return notesOwner.Clear() }
	}
	builtinSpecs := []runtimemodule.ThreadFactorySpec{
		{ID: memory.ModuleID, Enabled: cfg.ModuleEnabled(memory.ModuleID), New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
			client, caller := memoryClient(cfg, threadState.ID, opts.memoryAssignment)
			var markActive func(context.Context) error
			if opts.memoryAssignment == nil {
				feed := &memory.SourceFeed{AgentDir: cfg.RuntimePaths().StateDir, Client: func(id string) (memoryclient.API, memoryclient.Caller) {
					client, caller := memoryClient(cfg, id, nil)
					return client, caller
				}}
				markActive = func(ctx context.Context) error { return feed.MarkActive(ctx, threadState.ID) }
			}
			return memory.New(memory.Options{API: client, Caller: caller, Thread: threadState, MarkActive: markActive}), nil
		}},
		{ID: fleetmanagement.ModuleID,
			Enabled: cfg.ModuleEnabled(fleetmanagement.ModuleID) && cfg.FleetClientProfile == fleetclient.ProfileSupervisor && threadState != nil && threadState.ID == thread.MainID,
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return fleetmanagement.New(fleetclient.New(cfg.HomeJuexDir, cfg.FleetClientProfile, cfg.AgentID)), nil
			},
		},
		{
			ID:      inputtracking.ModuleID,
			Enabled: cfg.ModuleEnabled(inputtracking.ModuleID),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return inputtracking.New(engine), nil
			},
		},
		{
			ID:      chunkmodule.ModuleID,
			Enabled: cfg.ModuleEnabled(chunkmodule.ModuleID),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				paths := cfg.RuntimePaths()
				return chunkmodule.New(chunkmodule.Options{WorkDir: workDir, FilePolicy: sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: cfg.SandboxPolicy(), WorkDir: workDir, AgentStateDir: paths.StateDir, ReadOnlyPaths: []string{paths.MediaDir}})}), nil
			},
		},
		{
			ID:      contextcontrol.ModuleID,
			Enabled: cfg.ModuleEnabled(string(contextcontrol.ModuleID)),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return contextcontrol.New(engine), nil
			},
		},
		{
			ID:      operatingcontext.ModuleID,
			Enabled: cfg.ModuleEnabled(operatingcontext.ModuleID),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return &operatingcontext.Module{WorkDir: workDir}, nil
			},
		},
		{
			ID:         scratchpad.ModuleID,
			Inspection: scratchpad.Inspection(),
			Enabled:    cfg.ModuleEnabled(scratchpad.ModuleID),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return &scratchpad.Module{WorkDir: workDir}, nil
			},
		},
		{
			ID:            tasksmodule.ModuleID,
			Inspection:    tasksmodule.Inspection(),
			OwnsResources: true,
			Enabled:       cfg.ModuleEnabled(string(tasksmodule.ModuleID)),
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				tasksState := opts.tasksState
				if tasksState == nil {
					tasksState = tasksStateStore(threadState)
				}
				return tasksmodule.NewWithOptions(tasksState, tasksmodule.ModuleOptions{
					EnableContinuation:   opts.tasksContinuation,
					ContinuationDeferrer: opts.tasksContinuationDeferrer,
					EventSink:            eventSink,
					CurrentTurnID:        currentTurnID,
					OnAllTasksDone:       clearCompletedNotes,
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
				notesOwner = notesmodule.NewWithOptions(notes, notesmodule.Options{
					EventSink:     eventSink,
					CurrentTurnID: currentTurnID,
				})
				return notesOwner, nil
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
					TasksState:            tasksState,
				}), nil
			},
		})
	}
	combined := append(builtinSpecs, extra...)
	if opts.memoryAssignment != nil {
		for i := range combined {
			combined[i].Enabled = combined[i].Enabled && combined[i].ID == memory.ModuleID
		}
	}
	return combined
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
