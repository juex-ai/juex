package toolcontracts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/command"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	"github.com/juex-ai/juex/tests/testsupport/toolset"
)

func TestDefaultBuiltinToolGroups(t *testing.T) {
	registry := toolcore.NewRegistry()
	toolset.Register(registry, toolset.Options{Shell: contractsToolstestFakeShellProfile()})
	definitions := registry.List()
	want := map[string]toolcore.ToolGroup{
		"read":                toolcore.ToolGroupFile,
		"write":               toolcore.ToolGroupFile,
		"edit":                toolcore.ToolGroupFile,
		"apply_patch":         toolcore.ToolGroupFile,
		"write_begin":         toolcore.ToolGroupChunkedWrite,
		"write_chunk":         toolcore.ToolGroupChunkedWrite,
		"write_commit":        toolcore.ToolGroupChunkedWrite,
		"write_abort":         toolcore.ToolGroupChunkedWrite,
		"exec_command":        toolcore.ToolGroupShell,
		"list_shell_sessions": toolcore.ToolGroupShell,
		"write_stdin":         toolcore.ToolGroupShell,
		"grep":                toolcore.ToolGroupSearch,
	}
	if len(definitions) != len(want) {
		t.Fatalf("definition count = %d, want %d", len(definitions), len(want))
	}
	for _, definition := range definitions {
		if got, ok := want[definition.Name]; !ok {
			t.Errorf("unexpected builtin definition %q", definition.Name)
		} else if definition.Group != got {
			t.Errorf("%s group = %q, want %q", definition.Name, definition.Group, got)
		}

	}
}

func TestRegisterBuiltinsDefaultProviderToolSet(t *testing.T) {
	r := toolcore.NewRegistry()
	contractsToolstestRegisterTestBuiltins(r, t.TempDir())

	var names []string
	for _, tool := range r.List() {
		names = append(names, tool.Name)
	}
	want := []string{
		"apply_patch",
		"edit",
		"exec_command",
		"grep",
		"list_shell_sessions",
		"read",
		"write",
		"write_abort",
		"write_begin",
		"write_chunk",
		"write_commit",
		"write_stdin",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("builtin tools = %v, want %v", names, want)
	}
}

func TestBuiltins_RelativeWorkDirIsCapturedAsAbsolute(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "workspace")
	if err := os.MkdirAll(filepath.Join(workDir, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "music", "README.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)

	r := toolcore.NewRegistry()
	contractsToolstestRegisterTestBuiltins(r, "workspace")
	t.Chdir(t.TempDir())

	out, err := r.Call(context.Background(), "read", map[string]any{"path": "music/README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "rules" {
		t.Fatalf("read output = %q, want file from original workdir", out)
	}
}

func TestSpecs_OrderedAndComplete(t *testing.T) {
	r := toolcore.NewRegistry()
	contractsToolstestRegisterTestBuiltins(r, "")
	specs := r.Specs()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	want := []string{"apply_patch", "edit", "exec_command", "grep", "list_shell_sessions", "read", "write", "write_abort", "write_begin", "write_chunk", "write_commit", "write_stdin"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %v", want, names)
	}
}

func contractsToolstestFakeShellProfile() command.ShellProfile {
	return command.ShellProfile{
		Profile:   "fake",
		Family:    "posix",
		Binary:    os.Args[0],
		Args:      []string{"-test.run=TestShellHelperProcess", "--"},
		PathStyle: "posix",
	}
}

func contractsToolstestRegisterTestBuiltins(r *toolcore.Registry, workDir string) {
	toolset.Register(r, toolset.Options{WorkDir: workDir, MediaDir: contractsToolstestTestMediaDir(workDir), Shell: command.DefaultShellProfile()})
}

func contractsToolstestTestMediaDir(workDir string) string { return filepath.Join(workDir, "media") }
