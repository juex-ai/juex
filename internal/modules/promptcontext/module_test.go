package promptcontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestGuidanceModuleContextLoadsOrderedSections(t *testing.T) {
	globalDir := t.TempDir()
	globalAgents := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(workspace, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "AGENTS.md"), []byte("local agents rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	sections, err := (&GuidanceModule{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       []string{workspace, agentsDir},
	}).Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 3 {
		t.Fatalf("sections = %+v, want global and two workspace sections", sections)
	}
	want := []struct {
		key    string
		label  string
		source string
		text   string
	}{
		{key: "agents_global", label: "Global AGENTS.md", source: "user", text: "global rule"},
		{label: "Workspace AGENTS.md", source: "project", text: "workspace rule"},
		{label: ".agents/AGENTS.md", source: "project", text: "local agents rule"},
	}
	for index, expected := range want {
		section := sections[index]
		if (expected.key != "" && section.Key != expected.key) || section.Label != expected.label || section.Source != expected.source || !strings.Contains(section.Text, expected.text) {
			t.Fatalf("section[%d] = %+v, want %+v", index, section, expected)
		}
		if section.Projection != runtimemodule.ContextProjectionSystemPrompt || section.Budget != runtimemodule.UnboundedContextBudget() {
			t.Fatalf("section[%d] projection/budget = %+v", index, section)
		}
	}
}

func TestGuidanceModuleDeduplicatesGlobalWorkspaceFile(t *testing.T) {
	home := t.TempDir()
	homeAgents := filepath.Join(home, ".agents")
	if err := os.MkdirAll(homeAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	globalAgents := filepath.Join(homeAgents, "AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("one global rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	sections, err := (&GuidanceModule{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       []string{home, homeAgents},
	}).Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Key != "agents_global" {
		t.Fatalf("sections = %+v, want one global AGENTS section", sections)
	}
}

func TestThreadContextModuleIncludesScratchpadAndOperatingContext(t *testing.T) {
	workDir := t.TempDir()
	scratchpadDir := filepath.Join(workDir, ".juex", "threads", "123456", "scratchpad")
	now := time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC)
	sections, err := (&ThreadContextModule{WorkDir: workDir, Now: func() time.Time { return now }}).Context(
		context.Background(),
		runtimemodule.ContextRequest{
			Purpose: runtimemodule.ContextPurposeProviderIteration,
			Thread:  &runtimemodule.ThreadContext{ScratchpadDir: scratchpadDir},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 {
		t.Fatalf("sections = %+v, want scratchpad and operating context", sections)
	}
	scratchpad := sections[0]
	if scratchpad.Key != "thread_scratchpad" || scratchpad.Path != scratchpadDir || !strings.Contains(scratchpad.Text, "workspace-relative path for `write_begin`: .juex/threads/123456/scratchpad") {
		t.Fatalf("scratchpad section = %+v", scratchpad)
	}
	for _, want := range []string{"not automatically added to context", "use `read` or `grep`", "before compaction"} {
		if !strings.Contains(scratchpad.Text, want) {
			t.Errorf("scratchpad section missing %q:\n%s", want, scratchpad.Text)
		}
	}
	operating := sections[1]
	for _, want := range []string{"- cwd: " + workDir, "- os:", "- time: 2026-05-01T12:30:45Z"} {
		if !strings.Contains(operating.Text, want) {
			t.Errorf("operating context missing %q:\n%s", want, operating.Text)
		}
	}
}

func TestThreadContextModuleIncludesShellProfile(t *testing.T) {
	sections, err := (&ThreadContextModule{
		Shell: ShellProfile{
			Profile:   "powershell",
			Family:    "powershell",
			Binary:    "pwsh",
			Args:      []string{"-NoProfile", "-Command"},
			PathStyle: "windows",
		},
		Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) },
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

func TestShellProfileFromConfigCopiesArgs(t *testing.T) {
	cfg := config.ShellProfile{
		Profile:       "custom",
		Family:        "posix",
		Binary:        "bash",
		Args:          []string{"-lc"},
		PathStyle:     "posix",
		HostPathStyle: "platform",
	}

	got := ShellProfileFromConfig(cfg)
	cfg.Args[0] = "-c"

	if got.Profile != "custom" || got.Family != "posix" || got.Binary != "bash" || got.PathStyle != "posix" || got.HostPathStyle != "platform" {
		t.Fatalf("ShellProfileFromConfig = %+v", got)
	}
	if len(got.Args) != 1 || got.Args[0] != "-lc" {
		t.Fatalf("args = %+v, want defensive copy", got.Args)
	}
}

func TestContextModulesIgnoreOtherPurposes(t *testing.T) {
	request := runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeThreadStart}
	for _, provider := range []runtimemodule.ContextProvider{&GuidanceModule{}, &ThreadContextModule{}} {
		sections, err := provider.Context(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(sections) != 0 {
			t.Fatalf("%T sections = %+v, want none", provider, sections)
		}
	}
}
