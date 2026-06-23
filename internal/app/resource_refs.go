package app

import (
	"fmt"
	"path/filepath"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/extensions"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/skills"
)

type appResourceRefs struct {
	SkillDirs  []skills.Dir
	MCPConfigs []mcpConfigRef
	Hooks      hooks.Config
}

type mcpConfigRef struct {
	Path            string
	Source          string
	ExtensionDir    string
	StrictConflicts bool
}

func resolveAppResourceRefs(cfg config.Config) (appResourceRefs, error) {
	paths := cfg.ResourcePaths()
	extResources, err := extensions.Discover(extensions.DiscoverOptions{
		HomeJuexDir:               cfg.HomeJuexDir,
		WorkDir:                   cfg.WorkDir,
		EnableUserGlobalResources: cfg.EnableUserGlobalResources,
	})
	if err != nil {
		return appResourceRefs{}, err
	}

	hookConfig, err := appendExtensionHooks(cfg.Hooks, extResources.HookFiles)
	if err != nil {
		return appResourceRefs{}, err
	}

	return appResourceRefs{
		SkillDirs:  skillDirRefs(paths, extResources.SkillDirs),
		MCPConfigs: mcpConfigRefs(paths, extResources.MCPConfigs),
		Hooks:      hookConfig,
	}, nil
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
	refs, err := resolveAppResourceRefs(cfg)
	if err != nil {
		return nil, err
	}
	configs, _, _, err := loadMCPConfigRefs(refs.MCPConfigs, workDir)
	return configs, err
}
