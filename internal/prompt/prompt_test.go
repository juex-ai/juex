package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/skills"
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

	// skills dir
	skillRoot := t.TempDir()
	skillDir := filepath.Join(skillRoot, "x")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: x\ndescription: do X\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := skills.NewLoader(skillRoot)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}

	// memory store
	store := memory.NewStore(t.TempDir())
	if err := store.Write(memory.Entry{Name: "no-emoji", Description: "Never use emoji", Type: "feedback", Body: "Reason."}); err != nil {
		t.Fatal(err)
	}

	b := &Builder{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       []string{root, subdir},
		Memory:             store,
		Skills:             loader,
		Now:                func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}

	got := b.Build()
	mustContain(t, got, "project rule")
	mustContain(t, got, "subdir rule")
	mustContain(t, got, "global rule")
	mustContain(t, got, "Available Skills")
	mustContain(t, got, "do X")
	mustContain(t, got, "Memory")
	mustContain(t, got, "no-emoji")
	mustContain(t, got, "Operating Context")
	mustContain(t, got, "2026-05-01")
}

func TestBuilder_EmptySourcesSkipped(t *testing.T) {
	b := &Builder{
		AgentsMDDirs: []string{t.TempDir()}, // no AGENTS.md
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
	got := b.Build()
	if strings.Contains(got, "Available Skills") {
		t.Errorf("should not have skills section: %q", got)
	}
	if strings.Contains(got, "## Memory") {
		t.Errorf("should not have memory section")
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
	b := &Builder{
		AgentsMDDirs: []string{rootA, rootB},
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
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

	b := &Builder{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       []string{t.TempDir()}, // empty
		Now:                func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
	got := b.Build()
	mustContain(t, got, "only-global-rule")
	mustContain(t, got, "Operating Context")
}

func TestBuilder_OnlyProjectAgentsMD(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("only-project-rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := &Builder{
		AgentsMDDirs: []string{root},
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
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

	b := &Builder{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       []string{root, projectAgents},
		Now:                func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) },
	}
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

func TestBuilder_OperatingContextHasCwdOSAndTime(t *testing.T) {
	b := &Builder{
		AgentsMDDirs: []string{t.TempDir()},
		Now:          func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) },
	}
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
	b := &Builder{
		AgentsMDDirs: []string{t.TempDir()},
		WorkDir:      workDir,
		Now:          func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) },
	}

	got := b.Build()
	mustContain(t, got, "- cwd: "+workDir)
	if strings.Contains(got, "- cwd: "+processDir) {
		t.Fatalf("operating context used process cwd instead of workdir:\n%s", got)
	}
}

func TestBuilder_OperatingContextIncludesShellProfile(t *testing.T) {
	b := &Builder{
		AgentsMDDirs: []string{t.TempDir()},
		Shell: ShellProfile{
			Profile:   "powershell",
			Family:    "powershell",
			Binary:    "pwsh",
			Args:      []string{"-NoProfile", "-Command"},
			PathStyle: "windows",
		},
		Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) },
	}

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

func TestBuilder_OperatingContextNormalizesRelativeWorkDir(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	if err := os.MkdirAll("workspace", 0o755); err != nil {
		t.Fatal(err)
	}
	b := &Builder{
		AgentsMDDirs: []string{t.TempDir()},
		WorkDir:      "workspace",
		Now:          func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) },
	}

	got := b.Build()
	want := filepath.Join(base, "workspace")
	mustContain(t, got, "- cwd: "+want)
	if strings.Contains(got, "- cwd: workspace") {
		t.Fatalf("operating context kept relative workdir:\n%s", got)
	}
}

func TestBuilder_MemorySectionRendersAllEntries(t *testing.T) {
	store := memory.NewStore(t.TempDir())
	if err := store.Write(memory.Entry{Name: "one", Description: "first desc", Type: "feedback", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(memory.Entry{Name: "two", Description: "second desc", Type: "user", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(memory.Entry{Name: "three", Description: "third desc", Type: "project", Body: "b"}); err != nil {
		t.Fatal(err)
	}

	b := &Builder{
		AgentsMDDirs: []string{t.TempDir()},
		Memory:       store,
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
	got := b.Build()
	for _, want := range []string{"## Memory", "first desc", "second desc", "third desc", "feedback", "user", "project"} {
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

	b := &Builder{
		AgentsMDDirs: []string{root},
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
	got := b.Build()
	if !strings.Contains(got, "---") {
		t.Fatalf("expected --- divider between sections, got:\n%s", got)
	}
}

func TestBuilder_RebuildsFreshEachCall(t *testing.T) {
	// Memory writes between Build() calls must be reflected.
	root := t.TempDir()
	store := memory.NewStore(t.TempDir())
	b := &Builder{
		AgentsMDDirs: []string{root},
		Memory:       store,
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}

	first := b.Build()
	if strings.Contains(first, "added-after") {
		t.Fatal("entry should not be present yet")
	}
	if err := store.Write(memory.Entry{Name: "added-after", Description: "added-after", Type: "feedback", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	second := b.Build()
	if !strings.Contains(second, "added-after") {
		t.Fatalf("rebuild missed new memory entry:\n%s", second)
	}
}

func mustContain(t *testing.T, hay, needle string) {
	t.Helper()
	if !strings.Contains(hay, needle) {
		t.Errorf("expected %q in:\n%s", needle, hay)
	}
}
