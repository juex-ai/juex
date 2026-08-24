package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/session"
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
		"side-sessions",
		"observables",
		"mcp",
		"session-context",
		"goal",
		"notes",
		"hooks",
	} {
		modules[id] = config.ModuleSettings{Enabled: false}
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config:   config.Config{WorkDir: work, Modules: modules},
		Provider: &bareScriptProvider{},
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.CloseAndWait(); err != nil {
			t.Errorf("close App: %v", err)
		}
	})

	if descriptors := application.Engine.RuntimeModules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Runtime Modules = %#v, want none", descriptors)
	}
	if descriptors := application.Engine.SessionRuntimeSnapshot().Modules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Session Modules = %#v, want none", descriptors)
	}
	if serving := application.Engine.Tools.List(); len(serving) != 0 {
		t.Fatalf("serving Tools = %#v, want none", serving)
	}

	if err := application.ReadRuntimeModuleSnapshot(func(active app.RuntimeModuleSnapshot) error {
		status, snapshotErr := app.NewRuntimeCatalogService(config.Config{WorkDir: work, Modules: modules}).Snapshot(app.RuntimeStatusOptions{ActiveModules: &active})
		if snapshotErr != nil {
			return snapshotErr
		}
		if len(status.Modules) != 0 || status.Tools.Count != 0 || status.MCP.Configured != 0 || status.Hooks.Configured != 0 || status.Skills.Count != 0 {
			t.Fatalf("disabled Module status retained contributions: %+v", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestModuleLifecycle_PrimarySessionReplacementPublishesNewScopedSet(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")}
	application, err := app.New(app.Options{
		Config:     cfg,
		Provider:   &bareScriptProvider{},
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.CloseAndWait(); err != nil {
			t.Errorf("close App: %v", err)
		}
	})

	before := application.Engine.SessionRuntimeSnapshot()
	if before.Session == nil || before.Modules == nil {
		t.Fatalf("initial Session runtime = %+v", before)
	}
	beforeDescriptors := before.Modules.Descriptors()
	beforeToolNames := before.Modules.ToolCatalog().Names()

	if err := application.SwitchToNewPrimarySession(); err != nil {
		t.Fatal(err)
	}
	after := application.Engine.SessionRuntimeSnapshot()
	if after.Session == nil || after.Modules == nil {
		t.Fatalf("replacement Session runtime = %+v", after)
	}
	if after.Session.ID == before.Session.ID || after.Modules == before.Modules {
		t.Fatalf("replacement reused old Session set: before=%s after=%s", before.Session.ID, after.Session.ID)
	}
	history, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != after.Session.ID {
		t.Fatalf("active history = %+v, want committed replacement %q", history.Active, after.Session.ID)
	}
	if got := after.Modules.Descriptors(); !equalModuleDescriptors(got, beforeDescriptors) {
		t.Fatalf("replacement descriptors = %#v, want %#v", got, beforeDescriptors)
	}
	if got := after.Modules.ToolCatalog().Names(); !equalStrings(got, beforeToolNames) {
		t.Fatalf("replacement Tool catalog = %#v, want %#v", got, beforeToolNames)
	}

	sessionContext := runtimemodule.SessionContext{
		ID:            after.Session.ID,
		Dir:           after.Session.Dir,
		ScratchpadDir: after.ScratchpadDir,
	}
	sections, err := after.Modules.Context(context.Background(), runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Session: &sessionContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) == 0 {
		t.Fatal("replacement Session set contributed no provider context")
	}
	for _, section := range sections {
		if section.ModuleID == "" || section.Scope != runtimemodule.ScopeSession || section.Purpose != runtimemodule.ContextPurposeProviderIteration || strings.TrimSpace(section.Source) == "" {
			t.Errorf("replacement context lacks Framework provenance: %+v", section)
		}
	}

	_, err = before.Modules.Context(context.Background(), runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Session: &runtimemodule.SessionContext{ID: before.Session.ID, Dir: before.Session.Dir, ScratchpadDir: before.ScratchpadDir},
	})
	if err == nil || !strings.Contains(err.Error(), "session set is closed") {
		t.Fatalf("old Session set Context error = %v, want closed set", err)
	}
}

func equalModuleDescriptors(left, right []runtimemodule.Descriptor) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
