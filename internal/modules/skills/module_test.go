package skillsmodule

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

func TestModuleContributesSkillToolsAndContext(t *testing.T) {
	workDir := t.TempDir()
	skillDir := filepath.Join(workDir, ".agents", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review carefully\n---\nFollow the checklist."), 0o644); err != nil {
		t.Fatal(err)
	}

	mod, err := New(Options{
		Dirs:    []skills.Dir{{Path: filepath.Dir(skillDir), Source: "project"}},
		WorkDir: workDir,
		Sandbox: sandbox.Policy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mod.ID() != ModuleID {
		t.Fatalf("ID() = %q, want %q", mod.ID(), ModuleID)
	}
	provided, err := mod.Tools(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	for _, tool := range provided {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"skill_search", "skill_load"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("missing %s", name)
		}
	}
	loaded, err := registry.Call(context.Background(), "skill_load", map[string]any{"name": "review"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Source: project", "Follow the checklist."} {
		if !strings.Contains(loaded, want) {
			t.Fatalf("skill_load result missing %q:\n%s", want, loaded)
		}
	}
	sections, err := mod.Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Key != "skills" || !strings.Contains(sections[0].Text, "review") {
		t.Fatalf("context sections = %#v", sections)
	}
}

func TestModulePreservesExtensionProvenance(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "memory")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: memory\ndescription: Recall context\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	mod, err := New(Options{
		Dirs:    []skills.Dir{{Path: root, Source: "ext:memory", StrictConflicts: true}},
		WorkDir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	all := mod.All()
	found := false
	for _, skill := range all {
		if skill.Name == "memory" {
			found = true
			if skill.Source != "ext:memory" {
				t.Fatalf("source = %q, want ext:memory", skill.Source)
			}
		}
	}
	if !found {
		t.Fatalf("memory skill not loaded: %#v", all)
	}
}

func TestToolDefinitionsRemainStable(t *testing.T) {
	definitions := ToolDefinitions()
	if len(definitions) != 2 || definitions[0].Name != "skill_search" || definitions[1].Name != "skill_load" {
		t.Fatalf("definitions = %#v", definitions)
	}
}
