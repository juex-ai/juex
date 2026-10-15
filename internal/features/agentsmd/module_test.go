package agentsmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func TestModuleContextLoadsOrderedSections(t *testing.T) {
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

	sections, err := (&Module{
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

func TestModuleDeduplicatesGlobalWorkspaceFile(t *testing.T) {
	home := t.TempDir()
	homeAgents := filepath.Join(home, ".agents")
	if err := os.MkdirAll(homeAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	globalAgents := filepath.Join(homeAgents, "AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("one global rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	sections, err := (&Module{
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
