package app

import (
	"context"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/modules/builtintools"
	skillsmodule "github.com/juex-ai/juex/internal/modules/skills"
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
}

func buildRuntimeModules(
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
	var composition runtimeModuleComposition
	specs := []runtimemodule.RuntimeFactorySpec{
		{
			ID:      builtintools.ModuleID,
			Enabled: true,
			New: func(ctx context.Context, _ runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
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
				return composition.builtinTools, nil
			},
		},
		{
			ID:      skillsmodule.ModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				var err error
				composition.skills, err = skillsmodule.New(skillsmodule.Options{
					Dirs:          resourceGraph.SkillDirs(),
					LoaderOptions: skillLoaderOptions(cfg),
					WorkDir:       runtimePaths.WorkDir,
					Sandbox:       cfg.SandboxPolicy(),
				})
				return composition.skills, err
			},
		},
	}
	set, err := runtimemodule.BuildRuntimeSet(ctx, specs, runtimeContext, runtimemodule.ToolContext{Runtime: runtimeContext})
	if err != nil {
		return runtimeModuleComposition{}, err
	}
	if err := set.StartRuntime(ctx, runtimeContext); err != nil {
		return runtimeModuleComposition{}, err
	}
	composition.set = set
	composition.runtimeContext = runtimeContext
	return composition, nil
}

func buildSessionModules(
	ctx context.Context,
	specs []runtimemodule.SessionFactorySpec,
	runtimeContext runtimemodule.RuntimeContext,
	sess *session.Session,
) (*runtimemodule.Set, error) {
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
