package prompt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/modules/promptcontext"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestBuilder_AllSourcesPresent(t *testing.T) {
	root := t.TempDir()
	// AGENTS.md at the project root
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("project rule: be helpful"), 0o644); err != nil {
		t.Fatal(err)
	}
	// AGENTS.md at a subdir (cwd)
	subdir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "AGENTS.md"), []byte("subdir rule: prefer brevity"), 0o644); err != nil {
		t.Fatal(err)
	}

	// global agents file
	globalDir := t.TempDir()
	globalAgents := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("global rule: be polite"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := &Builder{
		ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
			request := runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration}
			guidance, err := (&promptcontext.GuidanceModule{GlobalAgentsMDPath: globalAgents, AgentsMDDirs: []string{root, subdir}}).Context(context.Background(), request)
			if err != nil {
				return nil, err
			}
			runtimeSections, err := (&promptcontext.SessionContextModule{Now: func() time.Time {
				return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			}}).Context(context.Background(), request)
			if err != nil {
				return nil, err
			}
			return append(append(guidance, []runtimemodule.PromptSection{
				{Key: "skills", Label: "Available Skills", Source: "runtime", Text: "## Available Skills\n- x: do X", Projection: runtimemodule.ContextProjectionSystemPrompt, Budget: runtimemodule.UnboundedContextBudget()},
				{Key: "demo", Label: "Demo Module", Source: "runtime", Text: "## Demo Module\nmodule context", Projection: runtimemodule.ContextProjectionSystemPrompt, Budget: runtimemodule.UnboundedContextBudget()},
			}...), runtimeSections...), nil
		},
	}

	got := b.Build()
	mustContain(t, got, "project rule")
	mustContain(t, got, "subdir rule")
	mustContain(t, got, "global rule")
	mustContain(t, got, "Available Skills")
	mustContain(t, got, "do X")
	mustContain(t, got, "Demo Module")
	mustContain(t, got, "module context")
	mustContain(t, got, "Operating Context")
	mustContain(t, got, "2026-05-01")
}

func TestBuilder_EmptySourcesSkipped(t *testing.T) {
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.GuidanceModule{AgentsMDDirs: []string{t.TempDir()}},
		&promptcontext.SessionContextModule{Now: func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) }},
	)
	got := b.Build()
	if strings.Contains(got, "Available Skills") {
		t.Errorf("should not have skills section: %q", got)
	}
	mustContain(t, got, "Operating Context") // always present
}

func TestBuilder_AgentsMDOrderingDeterministic(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "AGENTS.md"), []byte("AAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "AGENTS.md"), []byte("BBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.GuidanceModule{AgentsMDDirs: []string{rootA, rootB}},
	)
	got := b.Build()
	posA := strings.Index(got, "AAA")
	posB := strings.Index(got, "BBB")
	if posA < 0 || posB < 0 {
		t.Fatalf("missing one: %q", got)
	}
	if posA > posB {
		t.Errorf("expected AAA before BBB; got: %q", got)
	}
}

func TestBuilder_OnlyGlobalAgentsMD(t *testing.T) {
	globalDir := t.TempDir()
	globalAgents := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("only-global-rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.GuidanceModule{GlobalAgentsMDPath: globalAgents, AgentsMDDirs: []string{t.TempDir()}},
		&promptcontext.SessionContextModule{Now: func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) }},
	)
	got := b.Build()
	mustContain(t, got, "only-global-rule")
	mustContain(t, got, "Operating Context")
}

func TestBuilder_OnlyProjectAgentsMD(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("only-project-rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.GuidanceModule{AgentsMDDirs: []string{root}},
	)
	got := b.Build()
	mustContain(t, got, "only-project-rule")
}

func TestBuilder_SectionsIncludeInspectableAgentsEntries(t *testing.T) {
	home := t.TempDir()
	globalAgents := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("project root rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectAgents := filepath.Join(root, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "AGENTS.md"), []byte("project agents rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.GuidanceModule{GlobalAgentsMDPath: globalAgents, AgentsMDDirs: []string{root, projectAgents}},
		&promptcontext.SessionContextModule{Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)
	sections := b.Sections()
	if len(sections) != 4 {
		t.Fatalf("sections = %+v", sections)
	}
	want := []struct {
		label  string
		source string
		path   string
		text   string
	}{
		{label: "Global AGENTS.md", source: "user", path: globalAgents, text: "global rule"},
		{label: "Workspace AGENTS.md", source: "project", path: filepath.Join(root, "AGENTS.md"), text: "project root rule"},
		{label: ".agents/AGENTS.md", source: "project", path: filepath.Join(projectAgents, "AGENTS.md"), text: "project agents rule"},
		{label: "Operating Context", source: "runtime", path: "", text: "2026-05-01T12:30:45Z"},
	}
	for i, w := range want {
		got := sections[i]
		if got.Label != w.label || got.Source != w.source || got.Path != w.path || !strings.Contains(got.Text, w.text) {
			t.Fatalf("section[%d] = %+v, want label=%q source=%q path=%q text containing %q", i, got, w.label, w.source, w.path, w.text)
		}
	}
}

func TestBuilder_ModuleSectionsPreserveProviderOrder(t *testing.T) {
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		staticContextProvider{sections: []runtimemodule.ContextSection{{
			Key: "active_shell_sessions", Label: "Active Shell Sessions", Source: "runtime", Text: "## Active Shell Sessions\n- session_id=7", Projection: runtimemodule.ContextProjectionSystemPrompt, Budget: runtimemodule.UnboundedContextBudget(),
		}}},
		&promptcontext.SessionContextModule{Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)

	sections := b.Sections()
	if len(sections) != 2 {
		t.Fatalf("sections = %+v, want runtime section and operating context", sections)
	}
	if sections[0].Key != "active_shell_sessions" || sections[0].Label != "Active Shell Sessions" || sections[0].Source != "runtime" {
		t.Fatalf("runtime section = %+v", sections[0])
	}
	if sections[1].Key != "operating_context" {
		t.Fatalf("section[1] = %+v, want operating context after runtime section", sections[1])
	}

	got := b.Build()
	mustContain(t, got, "## Active Shell Sessions")
	mustContain(t, got, "session_id=7")
	if strings.Contains(got, "Empty Runtime") {
		t.Fatalf("empty runtime section leaked into prompt:\n%s", got)
	}
	if strings.Index(got, "## Active Shell Sessions") > strings.Index(got, "## Operating Context") {
		t.Fatalf("runtime section should appear before operating context:\n%s", got)
	}
}

func TestBuilder_SessionScratchpadSection(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, ".juex", "sessions", "session-1", "scratchpad")
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration, Session: &runtimemodule.SessionContext{ScratchpadDir: dir}},
		&promptcontext.SessionContextModule{WorkDir: work, Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)

	sections := b.Sections()
	if len(sections) != 2 {
		t.Fatalf("sections = %+v, want scratchpad and operating context", sections)
	}
	got := sections[0]
	if got.Key != "session_scratchpad" || got.Label != "Session Scratchpad" || got.Source != "runtime" || got.Path != dir {
		t.Fatalf("scratchpad section = %+v", got)
	}
	for _, want := range []string{
		"## Session Scratchpad",
		dir,
		"workspace-relative path for `write_begin`: .juex/sessions/session-1/scratchpad",
		"not automatically added to context",
		"use `read` or `grep`",
		"Notes",
		"before compaction",
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("scratchpad section missing %q:\n%s", want, got.Text)
		}
	}
	if sections[1].Key != "operating_context" {
		t.Fatalf("section[1] = %+v, want operating context", sections[1])
	}
}

func TestBuilder_OperatingContextHasCwdOSAndTime(t *testing.T) {
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.SessionContextModule{Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)
	got := b.Build()
	for _, want := range []string{"cwd:", "os:", "time:", "2026-05-01T12:30:45Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("operating context missing %q in:\n%s", want, got)
		}
	}
}

func TestBuilder_OperatingContextUsesWorkDir(t *testing.T) {
	processDir := t.TempDir()
	t.Chdir(processDir)
	workDir := t.TempDir()
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.SessionContextModule{WorkDir: workDir, Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)

	got := b.Build()
	mustContain(t, got, "- cwd: "+workDir)
	if strings.Contains(got, "- cwd: "+processDir) {
		t.Fatalf("operating context used process cwd instead of workdir:\n%s", got)
	}
}

func TestBuilder_OperatingContextIncludesShellProfile(t *testing.T) {
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.SessionContextModule{Shell: promptcontext.ShellProfile{
			Profile:   "powershell",
			Family:    "powershell",
			Binary:    "pwsh",
			Args:      []string{"-NoProfile", "-Command"},
			PathStyle: "windows",
		}, Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)

	got := b.Build()
	for _, want := range []string{
		"- shell: powershell (pwsh)",
		"- shell_family: powershell",
		"- shell_path_style: windows",
		"Use the `exec_command` tool with powershell syntax.",
		"do not use POSIX heredocs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("operating context missing %q in:\n%s", want, got)
		}
	}
}

func TestBuilder_OperatingContextNormalizesRelativeWorkDir(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	if err := os.MkdirAll("workspace", 0o755); err != nil {
		t.Fatal(err)
	}
	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.SessionContextModule{WorkDir: "workspace", Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }},
	)

	got := b.Build()
	want := filepath.Join(base, "workspace")
	mustContain(t, got, "- cwd: "+want)
	if strings.Contains(got, "- cwd: workspace") {
		t.Fatalf("operating context kept relative workdir:\n%s", got)
	}
}

func TestBuilder_ModuleSectionRendersProvidedContent(t *testing.T) {
	b := &Builder{
		ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
			return []runtimemodule.PromptSection{{
				Key: "example", Label: "Example Module", Source: "runtime", Text: "## Example Module\nfirst desc\nsecond desc\nthird desc", Projection: runtimemodule.ContextProjectionSystemPrompt, Budget: runtimemodule.UnboundedContextBudget(),
			}}, nil
		},
	}
	got := b.Build()
	for _, want := range []string{"Example Module", "first desc", "second desc", "third desc"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuilder_SectionsSeparatedByDivider(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := builderFromProviders(
		runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration},
		&promptcontext.GuidanceModule{AgentsMDDirs: []string{root}},
		&promptcontext.SessionContextModule{Now: func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) }},
	)
	got := b.Build()
	if !strings.Contains(got, "---") {
		t.Fatalf("expected --- divider between sections, got:\n%s", got)
	}
}

func TestBuilder_RebuildsFreshEachCall(t *testing.T) {
	moduleText := "before"
	b := &Builder{
		ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
			return []runtimemodule.PromptSection{{Key: "dynamic", Label: "Dynamic", Source: "runtime", Text: moduleText, Projection: runtimemodule.ContextProjectionSystemPrompt, Budget: runtimemodule.UnboundedContextBudget()}}, nil
		},
	}

	first := b.Build()
	if !strings.Contains(first, "before") {
		t.Fatalf("initial module context missing:\n%s", first)
	}
	moduleText = "added-after"
	second := b.Build()
	if !strings.Contains(second, "added-after") {
		t.Fatalf("rebuild missed new module context:\n%s", second)
	}
}

func TestBuilderBuildWithErrorReturnsModuleContextFailure(t *testing.T) {
	b := &Builder{
		ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
			return nil, errors.New("context unavailable")
		},
	}
	if _, err := b.BuildWithError(); err == nil || !strings.Contains(err.Error(), "prompt: module context: context unavailable") {
		t.Fatalf("BuildWithError() error = %v", err)
	}
}

type staticContextProvider struct {
	sections []runtimemodule.ContextSection
}

func (p staticContextProvider) Context(context.Context, runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	return append([]runtimemodule.ContextSection(nil), p.sections...), nil
}

func builderFromProviders(request runtimemodule.ContextRequest, providers ...runtimemodule.ContextProvider) *Builder {
	return &Builder{ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
		var sections []runtimemodule.ContextSection
		for _, provider := range providers {
			provided, err := provider.Context(context.Background(), request)
			if err != nil {
				return nil, err
			}
			sections = append(sections, provided...)
		}
		return sections, nil
	}}
}

func mustContain(t *testing.T, hay, needle string) {
	t.Helper()
	if !strings.Contains(hay, needle) {
		t.Errorf("expected %q in:\n%s", needle, hay)
	}
}
