package mcp

import (
	"context"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestRuntimeModuleOwnsManagerOnlyAfterStart(t *testing.T) {
	mod := NewRuntimeModule(nil, ConnectOptions{})
	if mod.Manager() != nil {
		t.Fatal("runtime module constructed MCP manager before StartRuntime")
	}
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	manager := mod.Manager()
	if manager == nil {
		t.Fatal("runtime module did not publish started MCP manager")
	}
	if err := mod.QuiesceRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tools(); err == nil {
		t.Fatal("owned MCP manager remains usable after Module quiesce")
	}
	if err := mod.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInjectedMCPManagerIsNotClosedByModule(t *testing.T) {
	manager, err := NewManagerLayeredSoft(context.Background(), nil, ConnectOptions{})
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
	if _, err := manager.Tools(); err != nil {
		t.Fatalf("injected MCP manager was closed: %v", err)
	}
}
