package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/extensions"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/skills"
)

type mcpConfigRef struct {
	Path            string
	Source          string
	ExtensionDir    string
	StrictConflicts bool
}

type RuntimeResourceKind string

const (
	RuntimeResourceExtension RuntimeResourceKind = "extension"
	RuntimeResourceSkillDir  RuntimeResourceKind = "skill_dir"
	RuntimeResourceMCPConfig RuntimeResourceKind = "mcp_config"
	RuntimeResourceHookFile  RuntimeResourceKind = "hook_file"
)

type RuntimeResourceNode struct {
	Kind            RuntimeResourceKind
	Source          string
	Path            string
	ExtensionName   string
	ExtensionDir    string
	RequireTrust    bool
	StrictConflicts bool
	Precedence      int
}

type RuntimeResourceGraph struct {
	skillDirs  []skills.Dir
	mcpConfigs []mcpConfigRef
	hooks      hooks.Config
	nodes      []RuntimeResourceNode
}

func ResolveRuntimeResourceGraph(cfg config.Config) (RuntimeResourceGraph, error) {
	paths := cfg.ResourcePaths()
	extResources, err := extensions.Discover(extensions.DiscoverOptions{
		HomeJuexDir:               cfg.HomeJuexDir,
		WorkDir:                   cfg.WorkDir,
		EnableUserGlobalResources: cfg.EnableUserGlobalResources,
	})
	if err != nil {
		return RuntimeResourceGraph{}, err
	}

	hookConfig, err := appendExtensionHooks(cfg.Hooks, extResources.HookFiles)
	if err != nil {
		return RuntimeResourceGraph{}, err
	}

	skillDirs := skillDirRefs(paths, extResources.SkillDirs)
	mcpConfigs := mcpConfigRefs(paths, extResources.MCPConfigs)
	return RuntimeResourceGraph{
		skillDirs:  skillDirs,
		mcpConfigs: mcpConfigs,
		hooks:      hookConfig,
		nodes:      runtimeResourceNodes(paths, extResources),
	}, nil
}

func (g RuntimeResourceGraph) SkillDirs() []skills.Dir {
	return append([]skills.Dir(nil), g.skillDirs...)
}

func (g RuntimeResourceGraph) MCPConfigs() []mcpConfigRef {
	return append([]mcpConfigRef(nil), g.mcpConfigs...)
}

func (g RuntimeResourceGraph) HooksConfig() hooks.Config {
	return cloneHooksConfig(g.hooks)
}

func (g RuntimeResourceGraph) Nodes() []RuntimeResourceNode {
	return append([]RuntimeResourceNode(nil), g.nodes...)
}

func runtimeResourceNodes(paths config.ResourcePaths, extResources extensions.Resources) []RuntimeResourceNode {
	var nodes []RuntimeResourceNode
	if paths.UserGlobalResources && paths.HomeAgentsDir != "" {
		nodes = append(nodes,
			runtimeResourceNode(RuntimeResourceSkillDir, "user", filepath.Join(paths.HomeAgentsDir, "skills"), false, false),
			runtimeResourceNode(RuntimeResourceMCPConfig, "user", filepath.Join(paths.HomeAgentsDir, "mcp.json"), false, false),
		)
	}
	for _, ext := range extResources.Extensions {
		nodes = append(nodes, RuntimeResourceNode{
			Kind:          RuntimeResourceExtension,
			Source:        ext.Source,
			Path:          ext.Dir,
			ExtensionName: ext.Name,
			ExtensionDir:  ext.Dir,
			RequireTrust:  ext.Scope == extensions.ScopeProject,
			Precedence:    runtimeSourceRank(ext.Source),
		})
		for _, ref := range extResources.SkillDirs {
			if ref.ExtensionName == ext.Name {
				nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceSkillDir, ref, true))
			}
		}
		for _, ref := range extResources.MCPConfigs {
			if ref.ExtensionName == ext.Name {
				nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceMCPConfig, ref, true))
			}
		}
		for _, ref := range extResources.HookFiles {
			if ref.ExtensionName == ext.Name {
				nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceHookFile, ref, true))
			}
		}
	}
	if paths.ProjectAgentsDir != "" {
		nodes = append(nodes,
			runtimeResourceNode(RuntimeResourceSkillDir, "project", filepath.Join(paths.ProjectAgentsDir, "skills"), false, false),
			runtimeResourceNode(RuntimeResourceMCPConfig, "project", filepath.Join(paths.ProjectAgentsDir, "mcp.json"), false, false),
		)
	}
	return nodes
}

func runtimeResourceNode(kind RuntimeResourceKind, source, path string, requireTrust, strictConflicts bool) RuntimeResourceNode {
	return RuntimeResourceNode{
		Kind:            kind,
		Source:          source,
		Path:            path,
		RequireTrust:    requireTrust,
		StrictConflicts: strictConflicts,
		Precedence:      runtimeSourceRank(source),
	}
}

func runtimeExtensionResourceNode(kind RuntimeResourceKind, ref extensions.ResourceRef, strictConflicts bool) RuntimeResourceNode {
	node := runtimeResourceNode(kind, ref.Source, ref.Path, ref.RequireTrust, strictConflicts)
	node.ExtensionName = ref.ExtensionName
	node.ExtensionDir = ref.ExtensionDir
	return node
}

func cloneHooksConfig(cfg hooks.Config) hooks.Config {
	commands := make([]hooks.CommandHook, 0, len(cfg.Commands))
	for _, command := range cfg.Commands {
		command.Events = append([]hooks.EventName(nil), command.Events...)
		command.Tools = append([]string(nil), command.Tools...)
		command.Command = append([]string(nil), command.Command...)
		commands = append(commands, command)
	}
	return hooks.Config{Commands: commands}
}

func runtimeSourceLess(leftSource, leftName, rightSource, rightName string) bool {
	leftRank := runtimeSourceRank(leftSource)
	rightRank := runtimeSourceRank(rightSource)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	return leftName < rightName
}

func runtimeSourceRank(source string) int {
	switch source {
	case "project":
		return 0
	case "user":
		return 2
	default:
		if extensions.IsExtensionSource(source) {
			return 1
		}
		if strings.TrimSpace(source) == "" {
			return 4
		}
		return 3
	}
}

func skillDirRefs(paths config.ResourcePaths, extRefs []extensions.ResourceRef) []skills.Dir {
	var refs []skills.Dir
	if paths.UserGlobalResources && paths.HomeAgentsDir != "" {
		refs = append(refs, skills.Dir{
			Path:   filepath.Join(paths.HomeAgentsDir, "skills"),
			Source: "user",
		})
	}
	for _, ref := range extRefs {
		refs = append(refs, skills.Dir{
			Path:            ref.Path,
			Source:          ref.Source,
			StrictConflicts: true,
		})
	}
	if paths.ProjectAgentsDir != "" {
		refs = append(refs, skills.Dir{
			Path:   filepath.Join(paths.ProjectAgentsDir, "skills"),
			Source: "project",
		})
	}
	return refs
}

func mcpConfigRefs(paths config.ResourcePaths, extRefs []extensions.ResourceRef) []mcpConfigRef {
	var refs []mcpConfigRef
	if paths.UserGlobalResources && paths.HomeAgentsDir != "" {
		refs = append(refs, mcpConfigRef{
			Path:   filepath.Join(paths.HomeAgentsDir, "mcp.json"),
			Source: "user",
		})
	}
	for _, ref := range extRefs {
		refs = append(refs, mcpConfigRef{
			Path:            ref.Path,
			Source:          ref.Source,
			ExtensionDir:    ref.ExtensionDir,
			StrictConflicts: true,
		})
	}
	if paths.ProjectAgentsDir != "" {
		refs = append(refs, mcpConfigRef{
			Path:   filepath.Join(paths.ProjectAgentsDir, "mcp.json"),
			Source: "project",
		})
	}
	return refs
}

func appendExtensionHooks(base hooks.Config, refs []extensions.ResourceRef) (hooks.Config, error) {
	out := hooks.Config{Commands: append([]hooks.CommandHook(nil), base.Commands...)}
	names := map[string]string{}
	for _, command := range out.Commands {
		if command.Name != "" {
			names[command.Name] = command.Source
		}
	}
	for _, ref := range refs {
		cfg, err := hooks.LoadFileConfig(ref.Path, ref.Source, ref.RequireTrust)
		if err != nil {
			return hooks.Config{}, err
		}
		for _, command := range cfg.Commands {
			if prev, ok := names[command.Name]; ok {
				return hooks.Config{}, fmt.Errorf("extensions: duplicate hook %q from %s and %s", command.Name, prev, command.Source)
			}
			names[command.Name] = command.Source
			out.Commands = append(out.Commands, command)
		}
	}
	return out, nil
}

func loadMCPConfigRefs(refs []mcpConfigRef, workDir string) ([]mcp.Config, mcp.Config, map[string]string, error) {
	var configs []mcp.Config
	merged := mcp.Config{MCPServers: map[string]mcp.ServerSpec{}}
	sources := map[string]string{}
	strict := map[string]bool{}
	for _, ref := range refs {
		cfg, err := mcp.LoadConfig(ref.Path)
		if err != nil {
			return nil, mcp.Config{}, nil, err
		}
		cfg = mcp.PrepareConfigWithOptions(cfg, mcp.PrepareOptions{
			WorkDir:      workDir,
			ExtensionDir: ref.ExtensionDir,
		})
		if len(cfg.MCPServers) == 0 {
			continue
		}
		for name, spec := range cfg.MCPServers {
			if prevSource, ok := sources[name]; ok && (strict[name] || ref.StrictConflicts) {
				return nil, mcp.Config{}, nil, fmt.Errorf("extensions: duplicate MCP server %q from %s and %s", name, prevSource, ref.Source)
			}
			merged.MCPServers[name] = spec
			sources[name] = ref.Source
			strict[name] = ref.StrictConflicts
		}
		configs = append(configs, cfg)
	}
	return configs, merged, sources, nil
}

// LoadMCPConfigs resolves configured MCP resources, including extension MCP
// bundles, into runtime-ready configs for process-scoped startup.
func LoadMCPConfigs(cfg config.Config, workDir string) ([]mcp.Config, error) {
	graph, err := ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		return nil, err
	}
	configs, _, _, err := loadMCPConfigRefs(graph.MCPConfigs(), workDir)
	return configs, err
}
