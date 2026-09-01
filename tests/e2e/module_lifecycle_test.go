package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/thread"
)

func TestModuleLifecycle_AllCompiledModulesDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	modules := config.ModulePolicy{}
	for _, id := range []string{
		"builtin-tools",
		"project-guidance",
		"skills",
		"worker-threads",
		"observables",
		"mcp",
		"context-control",
		"thread-context",
		"goal",
		"notes",
		"hooks",
	} {
		modules[id] = config.ModuleSettings{Enabled: false}
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config: config.Config{WorkDir: work, Modules: modules}, Provider: &bareScriptProvider{}, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })

	if descriptors := application.Engine.RuntimeModules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Runtime Modules = %#v, want none", descriptors)
	}
	if descriptors := application.Engine.ThreadRuntimeSnapshot().Modules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Thread Modules = %#v, want none", descriptors)
	}
	if serving := application.Engine.Tools.List(); len(serving) != 0 {
		t.Fatalf("serving Tools = %#v, want none", serving)
	}
}

func TestModuleLifecycle_NewGenerationKeepsThreadScopedSet(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config:   config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")},
		Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })

	before := application.Engine.ThreadRuntimeSnapshot()
	if before.Thread == nil || before.Thread.ID != thread.MainID || before.Modules == nil {
		t.Fatalf("initial Thread runtime = %+v", before)
	}
	if err := os.WriteFile(filepath.Join(before.ScratchpadDir, "durable.txt"), []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := application.Engine.ThreadRuntimeSnapshot()
	if after.Thread != before.Thread || after.Modules != before.Modules {
		t.Fatalf("/new replaced Thread-scoped runtime: before=%p/%p after=%p/%p", before.Thread, before.Modules, after.Thread, after.Modules)
	}
	if info := after.Thread.Info(); info.GenerationID != "g000002" {
		t.Fatalf("generation = %q, want g000002", info.GenerationID)
	}
	if data, err := os.ReadFile(filepath.Join(after.ScratchpadDir, "durable.txt")); err != nil || string(data) != "retained" {
		t.Fatalf("scratchpad after /new = %q, %v", data, err)
	}

	threadContext := runtimemodule.ThreadContext{ID: after.Thread.ID, Dir: after.Thread.Dir, ScratchpadDir: after.ScratchpadDir}
	sections, err := after.Modules.Context(context.Background(), runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Thread:  &threadContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) == 0 {
		t.Fatal("Thread set contributed no provider context")
	}
	for _, section := range sections {
		if section.ModuleID == "" || section.Scope != runtimemodule.ScopeThread || strings.TrimSpace(section.Source) == "" {
			t.Errorf("Thread context lacks provenance: %+v", section)
		}
	}
}
