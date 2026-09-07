package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/module/state"
)

type resourceFixtureModule struct{}

func (*resourceFixtureModule) ID() runtimemodule.ID { return "removed-fixture" }
func (*resourceFixtureModule) StartThread(_ context.Context, tc runtimemodule.ThreadContext) error {
	dir, err := state.Prepare(tc.Dir, state.Owner{Module: "removed-fixture", Scope: state.ScopeThread}, state.DiscardOnRemoval)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "opaque"), []byte("unparseable resource body"), 0600)
}
func (*resourceFixtureModule) CloseThread(context.Context) error { return nil }

func TestModuleResourcesRetireMissingFactoryAtColdStart(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, "state")}
	first, err := New(Options{Config: cfg, Provider: &stubProvider{}, DisableMCP: true, threadModuleFactories: []runtimemodule.ThreadFactorySpec{{
		ID: "removed-fixture", Enabled: true, OwnsResources: true,
		New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
			return &resourceFixtureModule{}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	path := state.Directory(first.Thread.Dir, state.Owner{Module: "removed-fixture", Scope: state.ScopeThread})
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("normal Close retired state", err)
	}
	// No constructor or state reader for the removed implementation is supplied.
	next, err := New(Options{Config: cfg, Provider: &stubProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.CloseAndWait() }()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing implementation retained state: %v", err)
	}
}
