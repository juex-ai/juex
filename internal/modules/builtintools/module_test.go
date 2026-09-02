package builtintools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

func TestModuleContributesDefaultBuiltinTools(t *testing.T) {
	mod := New(context.Background(), tools.BuiltinOptions{
		WorkDir:  t.TempDir(),
		Shell:    tools.DefaultShellProfile(),
		MediaDir: t.TempDir(),
	})
	if mod.ID() != ModuleID {
		t.Fatalf("ID() = %q, want %q", mod.ID(), ModuleID)
	}
	if mod.ShellSessions() != nil {
		t.Fatal("Module constructed an owned shell session manager before StartRuntime")
	}
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	provided, err := mod.Tools(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	want := tools.DefaultBuiltinToolDefinitions(tools.BuiltinDefinitionOptions{Shell: tools.DefaultShellProfile()})
	if len(provided) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(provided), len(want))
	}
	for i := range want {
		if provided[i].Name != want[i].Name {
			t.Fatalf("tool[%d] = %q, want %q", i, provided[i].Name, want[i].Name)
		}
	}
	if err := mod.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestModuleStartCreatesMediaRootBeforeShellSessions(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "agent", "media")
	if err := os.MkdirAll(filepath.Dir(mediaDir), 0o700); err != nil {
		t.Fatal(err)
	}
	mod := New(context.Background(), tools.BuiltinOptions{
		MediaDir: mediaDir,
		Shell:    tools.DefaultShellProfile(),
	})
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mod.CloseRuntime(context.Background()) }()
	if info, err := os.Stat(mediaDir); err != nil || !info.IsDir() {
		t.Fatalf("Artifact root stat = %+v, %v", info, err)
	}
}

func TestModulePreservesInjectedShellSessionOwnership(t *testing.T) {
	sessions := tools.NewShellSessionManager(context.Background())
	mod := New(context.Background(), tools.BuiltinOptions{ShellSessions: sessions})
	if err := mod.StartRuntime(context.Background(), runtimemodule.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	if err := mod.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Injected resources remain the caller's responsibility.
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
}
