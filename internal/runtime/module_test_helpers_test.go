package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

type HookRunner = hooks.PolicyRunner

func installHookRunner(t *testing.T, engine *Engine, runner hooks.PolicyRunner) {
	t.Helper()
	if engine == nil {
		t.Fatal("hook module requires an engine")
	}
	base := hooks.Request{CWD: engine.WorkDir, WorkspaceRoots: []string{engine.WorkDir}}
	if engine.Session != nil {
		base.SessionID = engine.Session.ID
		base.ConversationPath = filepath.Join(engine.Session.Dir, "conversation.jsonl")
		base.EventsPath = filepath.Join(engine.Session.Dir, "events.jsonl")
	}
	mod := hooks.NewModule(runner, hooks.ModuleOptions{BaseRequest: base})
	set, err := runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
		ID:      hooks.ModuleID,
		Enabled: true,
		New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return mod, nil
		},
	}}, runtimemodule.RuntimeContext{WorkDir: engine.WorkDir}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	engine.RuntimeModules = set
	engine.RuntimeContext = runtimemodule.RuntimeContext{WorkDir: engine.WorkDir}
}

func (e *Engine) RunSessionStartHooks(ctx context.Context) error {
	return e.RunSessionStartPolicies(ctx)
}

func (e *Engine) queueHookRuntimeContext(results []hooks.Result) error {
	contexts := make([]runtimemodule.PolicyContext, 0, len(results))
	for _, result := range results {
		if result.ExitCode != 0 || result.Stdout == "" {
			continue
		}
		name := result.Hook.Name
		if name == "" {
			name = "hook"
		}
		contexts = append(contexts, runtimemodule.PolicyContext{
			Label: "Hook additional context (" + name + "):\n",
			Text:  result.Stdout,
		})
	}
	return e.queuePolicyRuntimeContext(contexts)
}

func appendHookAdditionalContext(msg llm.Message, results []hooks.Result) llm.Message {
	msg.Blocks = append([]llm.Block(nil), msg.Blocks...)
	for _, result := range results {
		if result.ExitCode != 0 || result.Stdout == "" {
			continue
		}
		name := result.Hook.Name
		if name == "" {
			name = "hook"
		}
		msg.Blocks = append(msg.Blocks, llm.Block{
			Type: llm.BlockText,
			Text: "Hook additional context (" + name + "):\n" + result.Stdout,
		})
	}
	return msg
}

func newTestPromptBuilder(workDir string, now func() time.Time) *prompt.Builder {
	provider := &prompt.SessionContextModule{WorkDir: workDir, Now: now}
	return &prompt.Builder{ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
		return provider.Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	}}
}

func installModuleTools(t *testing.T, registry *tools.Registry, providers ...runtimemodule.ToolProvider) {
	t.Helper()
	for _, provider := range providers {
		provided, err := provider.Tools(context.Background(), runtimemodule.ToolContext{})
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range provided {
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func installSessionStateModules(t *testing.T, engine *Engine) {
	installSessionStateModulesWithGoalOptions(t, engine, GoalModuleOptions{EnableContinuation: true})
}

func installSessionStateModulesWithGoalOptions(t *testing.T, engine *Engine, goalOptions GoalModuleOptions) {
	t.Helper()
	if engine == nil || engine.Session == nil {
		t.Fatal("session state modules require an attached session")
	}
	sessionContext := runtimemodule.SessionContext{
		ID:            engine.Session.ID,
		Dir:           engine.Session.Dir,
		ScratchpadDir: engine.Session.ScratchpadDir(),
	}
	set, err := runtimemodule.BuildSessionSet(context.Background(), []runtimemodule.SessionFactorySpec{
		{ID: GoalModuleID, Enabled: true, New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
			return NewGoalModuleWithOptions(engine, goalOptions), nil
		}},
		{ID: NotesModuleID, Enabled: true, New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
			return NewNotesModule(engine), nil
		}},
	}, sessionContext, runtimemodule.ToolContext{Session: &sessionContext})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range set.ToolCatalog().Entries() {
		if err := engine.Tools.Register(entry.Tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.ReplaceSessionRuntimeBundle(engine.Session, SessionRuntimeReplacement{Modules: set, Tools: engine.Tools}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.CloseSession(context.Background()) })
}

type fixedGoalContinuationDeferrer bool

func (d fixedGoalContinuationDeferrer) ShouldDeferGoalContinuation() bool {
	return bool(d)
}

type panicGoalContinuationDeferrer struct {
	t *testing.T
}

func (d panicGoalContinuationDeferrer) ShouldDeferGoalContinuation() bool {
	d.t.Helper()
	d.t.Fatal("wait-for-user Goal consulted the continuation deferrer")
	return true
}
