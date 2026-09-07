package mcp

import (
	"context"
	"errors"
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

func TestRuntimeModuleRollbackDiscardsUnactivatedNotifications(t *testing.T) {
	delivered := false
	mod := NewRuntimeModule(nil, ConnectOptions{OnNotification: func(Notification) { delivered = true }})
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	mod.options.OnNotification(Notification{Content: "during discovery"})
	if delivered {
		t.Fatal("notification delivered during preparation")
	}
	if err := mod.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	mod.options.OnNotification(Notification{Content: "late transport notification"})
	if err := mod.ActivateRuntime(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("activation after rollback = %v", err)
	}
	if delivered {
		t.Fatal("rolled-back notification was delivered")
	}
}

func TestInjectedMCPManagerIsNotClosedByModule(t *testing.T) {
	manager, err := NewManagerLayeredSoft(context.Background(), nil, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close injected MCP manager: %v", err)
		}
	})
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
