package app

import (
	"context"
	"path/filepath"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/modules/builtintools"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	skillsmodule "github.com/juex-ai/juex/internal/modules/skills"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

type runtimeModuleComposition struct {
	set            *runtimemodule.Set
	builtinTools   *builtintools.Module
	skills         *skillsmodule.Module
	constructed    *constructedRuntimeModules
	runtimeContext runtimemodule.RuntimeContext
	specs          []runtimemodule.RuntimeFactorySpec
}

type constructedRuntimeModules struct {
	builtinTools *builtintools.Module
	skills       *skillsmodule.Module
}

type sessionModuleOptions struct {
	hookRunner               hooks.PolicyRunner
	hookBaseRequest          hooks.Request
	goalState                *workmem.GoalStateStore
	notes                    *workmem.NotesStore
	goalContinuation         bool
	goalContinuationDeferrer juexruntime.GoalContinuationDeferrer
}

func prepareRuntimeModules(
	_ context.Context,
	cfg config.Config,
	resourceGraph RuntimeResourceGraph,
	runtimePaths config.RuntimePaths,
	runtimeEnvironment environment.Snapshot,
	sandboxRunner sandbox.Runner,
	chunkedWrites *tools.ChunkedWriteManager,
	toolTimeoutSeconds int,
) (runtimeModuleComposition, error) {
	runtimeContext := runtimemodule.RuntimeContext{
		ID:            cfg.AgentAddress.ID(),
		WorkDir:       runtimePaths.WorkDir,
		AgentStateDir: runtimePaths.StateDir,
		ArtifactDir:   runtimePaths.ArtifactDir,
	}
	constructed := &constructedRuntimeModules{}
	composition := runtimeModuleComposition{runtimeContext: runtimeContext, constructed: constructed}
	composition.specs = []runtimemodule.RuntimeFactorySpec{
		{
			ID:      builtintools.ModuleID,
			Enabled: cfg.ModuleEnabled(string(builtintools.ModuleID)),
			New: func(factoryCtx context.Context, _ runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				mod := builtintools.New(factoryCtx, tools.BuiltinOptions{
					WorkDir:            runtimePaths.WorkDir,
					Environment:        runtimeEnvironment,
					Shell:              toolsShellProfile(cfg.Shell),
					Sandbox:            cfg.SandboxPolicy(),
					SandboxRunner:      sandboxRunner,
					ToolTimeoutSeconds: toolTimeoutSeconds,
					ChunkedWrites:      chunkedWrites,
					AgentStateDir:      runtimePaths.StateDir,
					ArtifactDir:        runtimePaths.ArtifactDir,
				})
				constructed.builtinTools = mod
				return mod, nil
			},
		},
		{
			ID:      promptcontext.GuidanceModuleID,
			Enabled: cfg.ModuleEnabled(string(promptcontext.GuidanceModuleID)),
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return &promptcontext.GuidanceModule{
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

func compiledModuleIDs() []string {
	return []string{
		string(builtintools.ModuleID),
		string(promptcontext.GuidanceModuleID),
		string(skillsmodule.ModuleID),
		string(sideSessionModuleID),
		string(observable.ModuleID),
		string(mcp.ModuleID),
		string(promptcontext.SessionContextModuleID),
		string(juexruntime.GoalModuleID),
		string(juexruntime.NotesModuleID),
		string(hooks.ModuleID),
	}
}

// ValidateModuleConfig rejects unknown compiled Module IDs before any Module
// constructor or process-scoped feature startup can run.
func ValidateModuleConfig(cfg config.Config) error {
	return cfg.ValidateModuleIDs(compiledModuleIDs())
}

func (c *runtimeModuleComposition) sealAndStart(ctx context.Context, extra ...runtimemodule.RuntimeFactorySpec) error {
	specs := append(append([]runtimemodule.RuntimeFactorySpec(nil), c.specs...), extra...)
	set, err := runtimemodule.BuildAndStartRuntimeSet(ctx, specs, c.runtimeContext, runtimemodule.ToolContext{Runtime: c.runtimeContext})
	if err != nil {
		return err
	}
	c.set = set
	if c.constructed != nil {
		c.builtinTools = c.constructed.builtinTools
		c.skills = c.constructed.skills
	}
	return nil
}

func buildSessionModules(
	ctx context.Context,
	cfg config.Config,
	specs []runtimemodule.SessionFactorySpec,
	runtimeContext runtimemodule.RuntimeContext,
	sess *session.Session,
	engine *juexruntime.Engine,
	workDir string,
	shell promptcontext.ShellProfile,
	shellSessions *tools.ShellSessionManager,
	opts sessionModuleOptions,
) (*runtimemodule.Set, error) {
	goalState := opts.goalState
	if goalState == nil && cfg.ModuleEnabled(string(juexruntime.GoalModuleID)) {
		goalState = goalStateStore(sess)
	}
	notes := opts.notes
	if notes == nil && cfg.ModuleEnabled(string(juexruntime.NotesModuleID)) {
		notes = notesStore(sess)
	}
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
	builtinSpecs := []runtimemodule.SessionFactorySpec{
		{
			ID:      promptcontext.SessionContextModuleID,
			Enabled: cfg.ModuleEnabled(string(promptcontext.SessionContextModuleID)),
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &promptcontext.SessionContextModule{WorkDir: workDir, Shell: shell, ShellSessions: shellSessions}, nil
			},
		},
		{
			ID:      juexruntime.GoalModuleID,
			Enabled: cfg.ModuleEnabled(string(juexruntime.GoalModuleID)),
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return juexruntime.NewGoalModuleWithOptions(goalState, juexruntime.GoalModuleOptions{
					EnableContinuation:   opts.goalContinuation,
					ContinuationDeferrer: opts.goalContinuationDeferrer,
					EventSink:            eventSink,
					CurrentTurnID:        currentTurnID,
				}), nil
			},
		},
		{
			ID:      juexruntime.NotesModuleID,
			Enabled: cfg.ModuleEnabled(string(juexruntime.NotesModuleID)),
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return juexruntime.NewNotesModuleWithOptions(notes, juexruntime.NotesModuleOptions{
					EventSink:     eventSink,
					CurrentTurnID: currentTurnID,
				}), nil
			},
		},
	}
	var set *runtimemodule.Set
	if opts.hookRunner != nil && cfg.ModuleEnabled(string(hooks.ModuleID)) {
		builtinSpecs = append(builtinSpecs, runtimemodule.SessionFactorySpec{
			ID:      hooks.ModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				base := opts.hookBaseRequest
				base.SessionID = sess.ID
				base.ConversationPath = filepath.Join(sess.Dir, "conversation.jsonl")
				base.EventsPath = filepath.Join(sess.Dir, "events.jsonl")
				return hooks.NewModule(opts.hookRunner, hooks.ModuleOptions{
					BaseRequest: base,
					GoalState:   func() []byte { return juexruntime.HookGoalStateFromModules(set) },
				}), nil
			},
		})
	}
	specs = append(builtinSpecs, specs...)
	sessionContext := sessionModuleContext(sess)
	var err error
	set, err = runtimemodule.BuildAndStartSessionSet(ctx, specs, sessionContext, runtimemodule.ToolContext{
		Runtime: runtimeContext,
		Session: &sessionContext,
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

func validateSessionModuleContext(
	ctx context.Context,
	runtimeSet *runtimemodule.Set,
	sessionSet *runtimemodule.Set,
	runtimeContext runtimemodule.RuntimeContext,
	sess *session.Session,
) error {
	sessionContext := sessionModuleContext(sess)
	_, err := runtimemodule.CollectContext(ctx, runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Runtime: runtimeContext,
		Session: &sessionContext,
	}, runtimeSet, sessionSet)
	return err
}

func sessionModuleContext(sess *session.Session) runtimemodule.SessionContext {
	if sess == nil {
		return runtimemodule.SessionContext{}
	}
	return runtimemodule.SessionContext{
		ID:            sess.ID,
		Dir:           sess.Dir,
		ScratchpadDir: sess.ScratchpadDir(),
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
