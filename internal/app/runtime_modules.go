package app

import (
	"context"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/modules/builtintools"
	skillsmodule "github.com/juex-ai/juex/internal/modules/skills"
	"github.com/juex-ai/juex/internal/prompt"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

type runtimeModuleComposition struct {
	set            *runtimemodule.Set
	builtinTools   *builtintools.Module
	skills         *skillsmodule.Module
	runtimeContext runtimemodule.RuntimeContext
	modules        []runtimemodule.Module
}

func prepareRuntimeModules(
	ctx context.Context,
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
	composition := runtimeModuleComposition{runtimeContext: runtimeContext}
	composition.builtinTools = builtintools.New(ctx, tools.BuiltinOptions{
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
	var err error
	composition.skills, err = skillsmodule.New(skillsmodule.Options{
		Dirs:          resourceGraph.SkillDirs(),
		LoaderOptions: skillLoaderOptions(cfg),
		WorkDir:       runtimePaths.WorkDir,
		Sandbox:       cfg.SandboxPolicy(),
	})
	if err != nil {
		_ = composition.builtinTools.CloseRuntime(context.Background())
		return runtimeModuleComposition{}, err
	}
	composition.modules = []runtimemodule.Module{
		composition.builtinTools,
		&prompt.GuidanceModule{
			GlobalAgentsMDPath: cfg.GlobalAgentsMDPath(),
			AgentsMDDirs:       cfg.AgentsMDDirs(),
		},
		composition.skills,
	}
	return composition, nil
}

func (c *runtimeModuleComposition) sealAndStart(ctx context.Context, extra ...runtimemodule.Module) error {
	modules := append(append([]runtimemodule.Module(nil), c.modules...), extra...)
	specs := make([]runtimemodule.RuntimeFactorySpec, 0, len(modules))
	for _, mod := range modules {
		moduleValue := mod
		specs = append(specs, runtimemodule.RuntimeFactorySpec{
			ID:      moduleValue.ID(),
			Enabled: true,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return moduleValue, nil
			},
		})
	}
	set, err := runtimemodule.BuildRuntimeSet(ctx, specs, c.runtimeContext, runtimemodule.ToolContext{Runtime: c.runtimeContext})
	if err != nil {
		return err
	}
	if err := set.StartRuntime(ctx, c.runtimeContext); err != nil {
		return err
	}
	c.set = set
	c.modules = modules
	return nil
}

func buildSessionModules(
	ctx context.Context,
	specs []runtimemodule.SessionFactorySpec,
	runtimeContext runtimemodule.RuntimeContext,
	sess *session.Session,
	engine *juexruntime.Engine,
	workDir string,
	shell prompt.ShellProfile,
	shellSessions *tools.ShellSessionManager,
) (*runtimemodule.Set, error) {
	builtinSpecs := []runtimemodule.SessionFactorySpec{
		{
			ID:      prompt.SessionContextModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &prompt.SessionContextModule{WorkDir: workDir, Shell: shell, ShellSessions: shellSessions}, nil
			},
		},
		{
			ID:      juexruntime.GoalModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return juexruntime.NewGoalModule(engine), nil
			},
		},
		{
			ID:      juexruntime.NotesModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return juexruntime.NewNotesModule(engine), nil
			},
		},
	}
	specs = append(builtinSpecs, specs...)
	sessionContext := sessionModuleContext(sess)
	set, err := runtimemodule.BuildSessionSet(ctx, specs, sessionContext, runtimemodule.ToolContext{
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
