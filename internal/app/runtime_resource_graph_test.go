package app

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/skills"
)

func TestResolveRuntimeResourceGraphSourceNodes(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	homeJuex := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(homeAgents, "skills", "user", "SKILL.md"), "---\nname: user\n---\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(homeAgents, "mcp.json"), `{"mcpServers":{"user":{"command":"user"}}}`)
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "skills", "project", "SKILL.md"), "---\nname: project\n---\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{"mcpServers":{"project":{"command":"project"}}}`)
	mustWriteRuntimeStatusFile(t, filepath.Join(homeJuex, "extensions", "chanwire", "skills", "ext", "SKILL.md"), "---\nname: ext\n---\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(homeJuex, "extensions", "chanwire", "mcp.json"), `{"mcpServers":{"ext":{"command":"ext"}}}`)

	graph, err := ResolveRuntimeResourceGraph(config.Config{
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		HomeJuexDir:               homeJuex,
		EnableUserAgentsResources: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := skillDirSources(graph.SkillDirs()), []string{"user", "ext:chanwire", "project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skill dir sources = %v, want %v", got, want)
	}
	if got, want := mcpConfigSources(graph.MCPConfigs()), []string{"user", "ext:chanwire", "project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mcp config sources = %v, want %v", got, want)
	}
	if got, want := nodeKindsAndSources(graph.Nodes()), []string{
		"skill_dir:user",
		"mcp_config:user",
		"extension:ext:chanwire",
		"skill_dir:ext:chanwire",
		"mcp_config:ext:chanwire",
		"skill_dir:project",
		"mcp_config:project",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nodes = %v, want %v", got, want)
	}
}

func TestResolveRuntimeResourceGraphExcludesUserResourcesWhenDisabled(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	homeJuex := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(homeAgents, "skills", "user", "SKILL.md"), "---\nname: user\n---\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(homeJuex, "extensions", "home", "skills", "ext", "SKILL.md"), "---\nname: ext\n---\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "skills", "project", "SKILL.md"), "---\nname: project\n---\n")

	graph, err := ResolveRuntimeResourceGraph(config.Config{
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		HomeJuexDir:               homeJuex,
		EnableUserAgentsResources: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := skillDirSources(graph.SkillDirs()), []string{"ext:home", "project"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skill dir sources = %v, want %v", got, want)
	}
}

func TestResolveRuntimeResourceGraphExtensionMetadataAndHooks(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "hooks.yaml"), `trusted: true
commands:
  - name: demo-hook
    events: [Stop]
    command: ["python3", "demo.py"]
`)

	graph, err := ResolveRuntimeResourceGraph(config.Config{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}

	hookConfig := graph.HooksConfig()
	if len(hookConfig.Commands) != 1 {
		t.Fatalf("hooks = %+v", hookConfig)
	}
	hook := hookConfig.Commands[0]
	if hook.Name != "demo-hook" || hook.Source != "ext:demo" {
		t.Fatalf("hook = %+v", hook)
	}

	var hookNode RuntimeResourceNode
	for _, node := range graph.Nodes() {
		if node.Kind == RuntimeResourceHookFile {
			hookNode = node
			break
		}
	}
	if hookNode.Source != "ext:demo" || hookNode.ExtensionName != "demo" || hookNode.ExtensionDir == "" {
		t.Fatalf("hook node metadata = %+v", hookNode)
	}
	if !hookNode.RequireTrust || !hookNode.StrictConflicts {
		t.Fatalf("hook node flags = %+v", hookNode)
	}
}

func TestRuntimeResourceSourcePrecedence(t *testing.T) {
	cases := []struct {
		source string
		rank   int
	}{
		{source: "project", rank: 0},
		{source: "ext:demo", rank: 1},
		{source: "user", rank: 2},
		{source: "custom", rank: 3},
		{source: "", rank: 4},
	}
	for _, tc := range cases {
		if got := runtimeSourceRank(tc.source); got != tc.rank {
			t.Fatalf("runtimeSourceRank(%q) = %d, want %d", tc.source, got, tc.rank)
		}
	}
}

func skillDirSources(dirs []skills.Dir) []string {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, dir.Source)
	}
	return out
}

func mcpConfigSources(refs []mcpConfigRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Source)
	}
	return out
}

func nodeKindsAndSources(nodes []RuntimeResourceNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, string(node.Kind)+":"+node.Source)
	}
	return out
}
