package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/prompt"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/tools"
)

type HookRunner = hooks.PolicyRunner

func installHookRunner(t *testing.T, engine *Engine, runner hooks.PolicyRunner) {
	t.Helper()
	if engine == nil {
		t.Fatal("hook module requires an engine")
	}
	base := hooks.Request{CWD: engine.WorkDir, WorkspaceRoots: []string{engine.WorkDir}}
	if engine.Thread != nil {
		base.ThreadID = engine.Thread.ID
		base.JournalPath = filepath.Join(engine.Thread.Dir, "journal.jsonl")
	}
	mod := hooks.NewModule(runner, hooks.ModuleOptions{BaseRequest: base})
	installRuntimeTestModules(t, engine, mod)
}

func installRuntimeTestModules(t *testing.T, engine *Engine, modules ...runtimemodule.Module) {
	t.Helper()
	if engine == nil {
		t.Fatal("runtime modules require an engine")
	}
	runtimeContext := runtimemodule.RuntimeContext{WorkDir: engine.WorkDir}
	specs := make([]runtimemodule.RuntimeFactorySpec, 0, len(modules))
	for _, module := range modules {
		module := module
		specs = append(specs, runtimemodule.RuntimeFactorySpec{
			ID:      module.ID(),
			Enabled: true,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return module, nil
			},
		})
	}
	set, err := runtimemodule.BuildRuntimeSet(context.Background(), specs, runtimeContext, runtimemodule.ToolContext{Runtime: runtimeContext})
	if err != nil {
		t.Fatal(err)
	}
	engine.RuntimeModules = set
	engine.RuntimeContext = runtimeContext
	t.Cleanup(func() { _ = set.CloseRuntime(context.Background()) })
}

func (e *Engine) RunThreadStartHooks(ctx context.Context) error {
	return e.RunThreadStartPolicies(ctx)
}

func (e *Engine) queuePolicyRuntimeContextFromHookResults(results []hooks.Result) error {
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

func appendPolicyAdditionalContext(msg llm.Message, results []hooks.Result) llm.Message {
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
	provider := &promptcontext.ThreadContextModule{WorkDir: workDir, Now: now}
	return &prompt.Builder{ModulePromptContext: func() ([]runtimemodule.ContextSection, error) {
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

func installThreadStateModules(t *testing.T, engine *Engine) (*workmem.GoalStateStore, *workmem.NotesStore) {
	return installThreadStateModulesWithGoalOptions(t, engine, GoalModuleOptions{EnableContinuation: true})
}

func installThreadStateModulesWithGoalOptions(t *testing.T, engine *Engine, goalOptions GoalModuleOptions) (*workmem.GoalStateStore, *workmem.NotesStore) {
	t.Helper()
	return installThreadStateModulesWithStoresAndGoalOptions(t, engine, nil, nil, goalOptions)
}

func installThreadStateModulesWithStores(
	t *testing.T,
	engine *Engine,
	goalState *workmem.GoalStateStore,
	notes *workmem.NotesStore,
) (*workmem.GoalStateStore, *workmem.NotesStore) {
	t.Helper()
	return installThreadStateModulesWithStoresAndGoalOptions(t, engine, goalState, notes, GoalModuleOptions{EnableContinuation: true})
}

func installThreadStateModulesWithStoresAndGoalOptions(
	t *testing.T,
	engine *Engine,
	goalState *workmem.GoalStateStore,
	notes *workmem.NotesStore,
	goalOptions GoalModuleOptions,
) (*workmem.GoalStateStore, *workmem.NotesStore) {
	t.Helper()
	if engine == nil || engine.Thread == nil {
		t.Fatal("thread state modules require an attached thread")
	}
	if goalState == nil {
		goalState = workmem.NewGoalStateStore(engine.Thread.Dir, workmem.GoalStateOptions{})
	}
	if notes == nil {
		notes = workmem.NewNotesStore(engine.Thread.Dir)
	}
	eventSink := func(event events.Event) error {
		if engine.Bus == nil {
			return nil
		}
		return engine.Bus.Emit(event)
	}
	currentTurnID := func() string { return engine.PendingInputStatus().TurnID }
	goalOptions.EventSink = eventSink
	goalOptions.CurrentTurnID = currentTurnID
	threadContext := runtimemodule.ThreadContext{
		ID:            engine.Thread.ID,
		Dir:           engine.Thread.Dir,
		ScratchpadDir: engine.Thread.ScratchpadDir(),
	}
	set, err := runtimemodule.BuildThreadSet(context.Background(), []runtimemodule.ThreadFactorySpec{
		{ID: GoalModuleID, Enabled: true, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
			return NewGoalModuleWithOptions(goalState, goalOptions), nil
		}},
		{ID: NotesModuleID, Enabled: true, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
			return NewNotesModuleWithOptions(notes, NotesModuleOptions{EventSink: eventSink, CurrentTurnID: currentTurnID}), nil
		}},
	}, threadContext, runtimemodule.ToolContext{Thread: &threadContext})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range set.ToolCatalog().Entries() {
		if err := engine.Tools.Register(entry.Tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.ReplaceThreadRuntimeBundle(engine.Thread, ThreadRuntimeReplacement{Modules: set, Tools: engine.Tools}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.CloseThread(context.Background()) })
	return goalState, notes
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
