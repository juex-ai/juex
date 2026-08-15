package memorymodule

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/tools"
)

func TestModuleContributesCompatiblePromptAndTools(t *testing.T) {
	dir := t.TempDir()
	store := memory.NewStore(dir)
	if err := store.Write(memory.Entry{
		Name: "preference", Description: "Keep answers concise", Type: "feedback", Body: "User preference.",
	}); err != nil {
		t.Fatal(err)
	}

	mod := New(dir)
	if mod.Name() != Name {
		t.Fatalf("Name() = %q, want %q", mod.Name(), Name)
	}
	sections, err := mod.PromptContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Key != "memory_files" || sections[0].Label != "Memory" ||
		!strings.Contains(sections[0].Text, "## Memory") || !strings.Contains(sections[0].Text, "Keep answers concise") {
		t.Fatalf("sections = %#v", sections)
	}

	registry := tools.NewRegistry()
	if err := mod.RegisterTools(registry); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory_write", "memory_search", "memory_delete"} {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("tool %q was not registered", name)
		}
	}
	out, err := registry.Call(context.Background(), "memory_search", map[string]any{"query": "concise"})
	if err != nil || !strings.Contains(out, "preference") {
		t.Fatalf("memory_search = %q, %v", out, err)
	}
}

func TestModuleOmitsEmptyMemoryPrompt(t *testing.T) {
	sections, err := New(t.TempDir()).PromptContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 0 {
		t.Fatalf("sections = %#v, want none", sections)
	}
}
