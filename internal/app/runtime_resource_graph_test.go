package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/extensions"
	"github.com/juex-ai/juex/internal/mcp"
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
	mustWriteRuntimeStatusFile(t, filepath.Join(homeJuex, "extensions", "chanwire", "observables.json"), `{"observables":[]}`)

	graph, err := ResolveRuntimeResourceGraph(config.Config{
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		HomeJuexDir:               homeJuex,
		Extensions:                allowExtensions("chanwire"),
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
		"observable_config:ext:chanwire",
		"skill_dir:project",
		"mcp_config:project",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nodes = %v, want %v", got, want)
	}
	refs := graph.ObservableConfigs()
	if len(refs) != 1 ||
		refs[0].Source != "ext:chanwire" ||
		refs[0].ExtensionRuntime.ExtensionDir == "" {
		t.Fatalf("observable config refs = %+v", refs)
	}
}

func TestResolveRuntimeResourceGraphUsesLayeredExtensionPolicyAndWinningBundle(t *testing.T) {
	userHome := t.TempDir()
	instanceHome := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	t.Setenv("JUEX_HOME", instanceHome)
	t.Setenv("CODEX_HOME", filepath.Join(userHome, "missing-codex-home"))
	for _, key := range []string{
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
		"PROVIDER_THINKING_EFFORT",
		"PROVIDER_CONTEXT_WINDOW",
	} {
		t.Setenv(key, "")
	}

	mustWriteRuntimeStatusFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "extensions:\n  allow: [shared]\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(instanceHome, "juex.yaml"), "skills:\n  include: []\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "juex.yaml"), "sandbox:\n  enabled: true\n")
	mustWriteRuntimeStatusFile(t, filepath.Join(userHome, ".juex", "extensions", "shared", "skills"), "invalid lower bundle")
	mustWriteRuntimeStatusFile(t, filepath.Join(instanceHome, "extensions", "shared", "mcp.json"), "{}")
	projectExtensionDir := filepath.Join(work, ".juex", "extensions", "shared")
	mustWriteRuntimeStatusFile(t, filepath.Join(projectExtensionDir, "hooks.yaml"), "trusted: true\ncommands: []\n")

	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    work,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var extensionNode RuntimeResourceNode
	for _, node := range graph.Nodes() {
		if node.Kind == RuntimeResourceExtension {
			extensionNode = node
			break
		}
	}
	if extensionNode.Source != "ext:shared" ||
		extensionNode.Path != projectExtensionDir ||
		!extensionNode.RequireTrust {
		t.Fatalf("winning extension node = %+v", extensionNode)
	}
	for _, ref := range graph.MCPConfigs() {
		if ref.Source == "ext:shared" {
			t.Fatalf("lower Fleet bundle leaked MCP config: %+v", graph.MCPConfigs())
		}
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
		Extensions:                allowExtensions("home"),
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

	graph, err := ResolveRuntimeResourceGraph(config.Config{WorkDir: work, Extensions: allowExtensions("demo")})
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

func TestLoadMCPConfigsPreparesAgentOwnedDataDirForSelectedLocalExtensionWithoutCreatingIt(t *testing.T) {
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	extensionDir := filepath.Join(home, "extensions", "demo")
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "mcp.json"), `{
  "mcpServers": {
    "local": {
      "command": "${JUEX_EXT_DIR}/bin/server",
      "args": ["--data", "$JUEX_EXT_DATA_DIR"],
      "env": {"DATA_COPY": "${JUEX_EXT_DATA_DIR}"}
    },
    "remote": {
      "type": "http",
      "url": "https://mcp.example.com/mcp"
    }
  }
}`)
	cfg := config.Config{
		WorkDir:      work,
		HomeJuexDir:  home,
		AgentAddress: address,
		Extensions:   allowExtensions("demo"),
	}
	graph, err := ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(address.StateDir(), "extensions", "demo")
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("resource discovery created data dir, stat error = %v", err)
	}
	var extensionNode RuntimeResourceNode
	for _, node := range graph.Nodes() {
		if node.Kind == RuntimeResourceExtension {
			extensionNode = node
			break
		}
	}
	if extensionNode.ExtensionDataDir != dataDir {
		t.Fatalf("extension node data dir = %q, want %q", extensionNode.ExtensionDataDir, dataDir)
	}

	_, preview, _, err := loadMCPConfigRefs(graph.MCPConfigs(), work, environment.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("status-style config preview created data dir, stat error = %v", err)
	}
	if _, ok := preview.MCPServers["local"].Env["JUEX_EXT_DATA_DIR"]; ok {
		t.Fatalf("status-style config preview injected data dir: %#v", preview.MCPServers["local"].Env)
	}

	configs, err := LoadMCPConfigs(cfg, work)
	if err != nil {
		t.Fatal(err)
	}
	merged := mcp.MergeConfigs(configs)
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("LoadMCPConfigs created data dir, stat error = %v", err)
	}
	local := merged.MCPServers["local"]
	if filepath.Clean(local.Command) != filepath.Join(extensionDir, "bin", "server") {
		t.Fatalf("local command = %q", local.Command)
	}
	if local.Env["JUEX_EXT_DATA_DIR"] != dataDir || local.Env["DATA_COPY"] != dataDir {
		t.Fatalf("local env = %#v", local.Env)
	}
	if remote := merged.MCPServers["remote"]; remote.Env != nil {
		t.Fatalf("remote environment leaked = %#v", remote.Env)
	}
}

func TestLoadMCPConfigRefsDoesNotCreateDataDirForRemoteOnlyExtension(t *testing.T) {
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(home, "extensions", "remote")
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "mcp.json"), `{
  "mcpServers": {
    "remote": {
      "type": "http",
      "url": "https://mcp.example.com/mcp"
    }
  }
}`)
	graph, err := ResolveRuntimeResourceGraph(config.Config{
		WorkDir:      t.TempDir(),
		HomeJuexDir:  home,
		AgentAddress: address,
		Extensions:   allowExtensions("remote"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadMCPConfigRefsForRuntime(graph.MCPConfigs(), t.TempDir(), environment.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(address.StateDir(), "extensions", "remote")
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("remote-only extension created data dir, stat error = %v", err)
	}
}

func TestLoadMCPConfigRefsDoesNotCreateDataDirWhenMixedExtensionPreparationFails(t *testing.T) {
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(home, "extensions", "mixed")
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "mcp.json"), `{
  "mcpServers": {
    "local": {"command": "local-server"},
    "remote": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {"Authorization": "Bearer ${MISSING_TOKEN}"}
    }
  }
}`)
	graph, err := ResolveRuntimeResourceGraph(config.Config{
		WorkDir:      t.TempDir(),
		HomeJuexDir:  home,
		AgentAddress: address,
		Extensions:   allowExtensions("mixed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = loadMCPConfigRefsForRuntime(graph.MCPConfigs(), t.TempDir(), environment.Snapshot{})
	if err == nil || !strings.Contains(err.Error(), "MISSING_TOKEN") {
		t.Fatalf("startup error = %v, want missing credential", err)
	}
	dataDir := filepath.Join(address.StateDir(), "extensions", "mixed")
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("failed extension preparation created data dir, stat error = %v", err)
	}
}

func TestLoadMCPConfigRefsDoesNotPrepareOverriddenLocalExtension(t *testing.T) {
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	extensionPath := filepath.Join(t.TempDir(), "extension-mcp.json")
	projectPath := filepath.Join(t.TempDir(), "project-mcp.json")
	mustWriteRuntimeStatusFile(t, extensionPath, `{"mcpServers":{"shared":{"command":"extension-server"}}}`)
	mustWriteRuntimeStatusFile(t, projectPath, `{"mcpServers":{"shared":{"command":"project-server"}}}`)
	context := newExtensionRuntimeContext(address, extensions.Extension{
		Name:   "demo",
		Dir:    filepath.Dir(extensionPath),
		Source: extensions.Source("demo"),
	})

	_, merged, _, err := loadMCPConfigRefsForRuntime([]mcpConfigRef{
		{Path: extensionPath, Source: extensions.Source("demo"), ExtensionRuntime: context},
		{Path: projectPath, Source: "project"},
	}, t.TempDir(), environment.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.MCPServers["shared"].Command; got != "project-server" {
		t.Fatalf("winning command = %q", got)
	}
	if _, err := os.Stat(context.DataDir); !os.IsNotExist(err) {
		t.Fatalf("overridden extension created data dir, stat error = %v", err)
	}
}

func TestResolveRuntimeResourceGraphStateFreePreviewHasNoExtensionDataDir(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "mcp.json"), `{"mcpServers":{"local":{"command":"server"}}}`)
	graph, err := ResolveRuntimeResourceGraph(config.Config{
		WorkDir:    work,
		Extensions: allowExtensions("demo"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes() {
		if node.ExtensionName == "demo" && node.ExtensionDataDir != "" {
			t.Fatalf("state-free node has data dir: %+v", node)
		}
	}
	_, merged, _, err := loadMCPConfigRefs(graph.MCPConfigs(), work, environment.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged.MCPServers["local"].Env["JUEX_EXT_DATA_DIR"]; ok {
		t.Fatalf("state-free preview injected JUEX_EXT_DATA_DIR: %#v", merged.MCPServers["local"].Env)
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
	graph, err := ResolveRuntimeResourceGraph(config.Config{WorkDir: work, Extensions: allowExtensions("remote")})
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

func allowExtensions(names ...string) config.ExtensionPolicy {
	return config.ExtensionPolicy{Allow: append([]string(nil), names...), Configured: true}
}
