package app

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
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

func TestLoadMCPConfigRefsPreparesRemoteExtensionCredentials(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "remote", "mcp.json"), `{
	"mcpServers": {
	    "search": {
	      "type": "http",
	      "url": "https://mcp.example.com/mcp",
	      "headers": {"Authorization": "Bearer ${REMOTE_MCP_TOKEN}"}
	    }
  }
}`)
	graph, err := ResolveRuntimeResourceGraph(config.Config{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := environment.Resolve(environment.Options{
		Inherited: []string{"REMOTE_MCP_TOKEN=extension-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	configs, merged, sources, err := loadMCPConfigRefs(graph.MCPConfigs(), work, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("configs = %d, want 1", len(configs))
	}
	server := merged.MCPServers["search"]
	if server.URL != "https://mcp.example.com/mcp" {
		t.Fatalf("URL = %q", server.URL)
	}
	if server.Headers["Authorization"].Value() != "Bearer extension-secret" {
		t.Fatalf("prepared headers = %+v", server.Headers)
	}
	if sources["search"] != "ext:remote" {
		t.Fatalf("source = %q, want ext:remote", sources["search"])
	}
}

func TestLoadMCPConfigRefsResolvesCredentialsAfterLayerOverrides(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "user-mcp.json")
	projectPath := filepath.Join(t.TempDir(), "project-mcp.json")
	mustWriteRuntimeStatusFile(t, userPath, `{
  "mcpServers": {
	    "shared": {
	      "type": "http",
	      "url": "https://mcp.example.com/mcp",
	      "headers": {"Authorization": "Bearer ${OVERRIDDEN_MISSING_TOKEN}"}
    }
  }
}`)
	mustWriteRuntimeStatusFile(t, projectPath, `{
  "mcpServers": {
    "shared": {"command": "project-server"}
  }
}`)
	snapshot, err := environment.Resolve(environment.Options{})
	if err != nil {
		t.Fatal(err)
	}

	configs, merged, sources, err := loadMCPConfigRefs([]mcpConfigRef{
		{Path: userPath, Source: "user"},
		{Path: projectPath, Source: "project"},
	}, t.TempDir(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("effective configs = %d, want 1", len(configs))
	}
	if got := merged.MCPServers["shared"].Command; got != "project-server" {
		t.Fatalf("winning command = %q", got)
	}
	if sources["shared"] != "project" {
		t.Fatalf("source = %q, want project", sources["shared"])
	}
}

func TestLoadMCPConfigRefsRejectsMissingCredentialOnWinningLayer(t *testing.T) {
	userPath := filepath.Join(t.TempDir(), "user-mcp.json")
	projectPath := filepath.Join(t.TempDir(), "project-mcp.json")
	mustWriteRuntimeStatusFile(t, userPath, `{
  "mcpServers": {
    "shared": {"command": "user-server"}
  }
}`)
	mustWriteRuntimeStatusFile(t, projectPath, `{
  "mcpServers": {
	    "shared": {
	      "type": "http",
	      "url": "https://mcp.example.com/mcp",
	      "headers": {"Authorization": "Bearer ${WINNING_MISSING_TOKEN}"}
    }
  }
}`)
	snapshot, err := environment.Resolve(environment.Options{})
	if err != nil {
		t.Fatal(err)
	}

	_, _, _, err = loadMCPConfigRefs([]mcpConfigRef{
		{Path: userPath, Source: "user"},
		{Path: projectPath, Source: "project"},
	}, t.TempDir(), snapshot)
	if err == nil || !strings.Contains(err.Error(), "WINNING_MISSING_TOKEN") {
		t.Fatalf("loadMCPConfigRefs() error = %v, want winning credential failure", err)
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
