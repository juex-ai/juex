package shelltools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

func TestModuleOwnsShellTools(t *testing.T) {
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
	want := []string{"exec_command", "list_shell_sessions", "write_stdin"}
	if len(provided) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(provided), len(want))
	}
	for i := range want {
		if provided[i].Name != want[i] {
			t.Fatalf("tool[%d] = %q, want %q", i, provided[i].Name, want[i])
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

func TestModuleIncludesShellProfile(t *testing.T) {
	sections, err := New(context.Background(), tools.BuiltinOptions{
		Shell: tools.ShellProfile{
			Profile:   "powershell",
			Family:    "powershell",
			Binary:    "pwsh",
			Args:      []string{"-NoProfile", "-Command"},
			PathStyle: "windows",
		},
	}).Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 {
		t.Fatalf("sections = %+v, want operating context", sections)
	}
	for _, want := range []string{
		"- shell: powershell (pwsh)",
		"- shell_family: powershell",
		"- shell_path_style: windows",
		"Use the `exec_command` tool with powershell syntax.",
		"do not use POSIX heredocs",
	} {
		if !strings.Contains(sections[0].Text, want) {
			t.Errorf("operating context missing %q:\n%s", want, sections[0].Text)
		}
	}
}
