package observable

import (
	"context"
	"path/filepath"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestRuntimeModuleDefersManagerConstructionUntilStart(t *testing.T) {
	dir := t.TempDir()
	mod := NewRuntimeModule(ManagerOptions{
		ConfigPath: filepath.Join(dir, "observables.yaml"),
		StateDir:   filepath.Join(dir, "state"),
		WorkDir:    dir,
	}, false)
	if mod.Manager() != nil {
		t.Fatal("runtime module constructed Observable manager before StartRuntime")
	}
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	manager := mod.Manager()
	if manager == nil {
		t.Fatal("runtime module did not publish started Observable manager")
	}
	if err := mod.QuiesceRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mod.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInjectedObservableManagerIsNotClosedByModule(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(ManagerOptions{
		ConfigPath: filepath.Join(dir, "observables.yaml"),
		StateDir:   filepath.Join(dir, "state"),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	mod := NewModule(manager)
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	if err := mod.QuiesceRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StatusByID("missing"); err == ErrManagerClosed {
		t.Fatal("injected Observable manager was closed")
	}
}
