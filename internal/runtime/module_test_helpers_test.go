package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/prompt"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

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
			return NewGoalModule(engine), nil
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
