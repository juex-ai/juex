package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/extensions"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/skills"
)

type mcpConfigRef struct {
	Path             string
	Source           string
	ExtensionRuntime ExtensionRuntimeContext
	StrictConflicts  bool
}

type observableConfigRef struct {
	Path             string
	Source           string
	ExtensionRuntime ExtensionRuntimeContext
}

type RuntimeResourceKind string

const (
	RuntimeResourceExtension        RuntimeResourceKind = "extension"
	RuntimeResourceSkillDir         RuntimeResourceKind = "skill_dir"
	RuntimeResourceMCPConfig        RuntimeResourceKind = "mcp_config"
	RuntimeResourceHookFile         RuntimeResourceKind = "hook_file"
	RuntimeResourceObservableConfig RuntimeResourceKind = "observable_config"
)

type RuntimeResourceNode struct {
	Kind             RuntimeResourceKind
	Source           string
	Path             string
	ExtensionName    string
	ExtensionDir     string
	ExtensionDataDir string
	RequireTrust     bool
	StrictConflicts  bool
	Precedence       int
}

type RuntimeExtensionDescriptor struct {
	Name         string
	Dir          string
	Source       string
	Scope        extensions.Scope
	RequireTrust bool
	Manifest     extensions.Manifest
	Runtime      ExtensionRuntimeContext
}

type RuntimeResourceGraph struct {
	extensions        []RuntimeExtensionDescriptor
	skillDirs         []skills.Dir
	mcpConfigs        []mcpConfigRef
	observableConfigs []observableConfigRef
	hooks             hooks.Config
	nodes             []RuntimeResourceNode
}

func ResolveRuntimeResourceGraph(cfg config.Config) (RuntimeResourceGraph, error) {
	paths := cfg.ResourcePaths()
	extensionPolicy := cfg.ExtensionPolicy()
	extResources, err := extensions.Discover(extensions.DiscoverOptions{
		Roots: []extensions.Root{
			{Path: paths.DefaultHomeExtensionsDir, Scope: extensions.ScopeDefaultHome},
			{Path: paths.HomeExtensionsDir, Scope: homeExtensionScope(paths)},
			{Path: paths.ProjectExtensionsDir, Scope: extensions.ScopeProject, RequireTrust: true},
		},
		AllowedNames: extensionPolicy.Allow,
	})
	if err != nil {
		return RuntimeResourceGraph{}, err
	}

	runtimeContexts := extensionRuntimeContexts(cfg, extResources.Extensions)
	hookConfig, err := appendExtensionHooks(cfg.Hooks, extResources.HookFiles, runtimeContexts)
	if err != nil {
		return RuntimeResourceGraph{}, err
	}

	skillDirs := skillDirRefs(paths, extResources.SkillDirs)
	mcpConfigs := mcpConfigRefs(paths, extResources.MCPConfigs, runtimeContexts)
	observableConfigs := observableConfigRefs(extResources.ObservableConfigs, runtimeContexts)
	return RuntimeResourceGraph{
		extensions:        runtimeExtensionDescriptors(extResources.Extensions, runtimeContexts),
		skillDirs:         skillDirs,
		mcpConfigs:        mcpConfigs,
		observableConfigs: observableConfigs,
		hooks:             hookConfig,
		nodes:             runtimeResourceNodes(paths, extResources, runtimeContexts),
	}, nil
}

func (g RuntimeResourceGraph) Extensions() []RuntimeExtensionDescriptor {
	return append([]RuntimeExtensionDescriptor(nil), g.extensions...)
}

func homeExtensionScope(paths config.ResourcePaths) extensions.Scope {
	if sameRuntimeResourcePath(paths.HomeExtensionsDir, paths.DefaultHomeExtensionsDir) {
		return extensions.ScopeDefaultHome
	}
	return extensions.ScopeInstanceHome
}

func sameRuntimeResourcePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func (g RuntimeResourceGraph) SkillDirs() []skills.Dir {
	return append([]skills.Dir(nil), g.skillDirs...)
}

func (g RuntimeResourceGraph) MCPConfigs() []mcpConfigRef {
	return append([]mcpConfigRef(nil), g.mcpConfigs...)
}

func (g RuntimeResourceGraph) ObservableConfigs() []observableConfigRef {
	return append([]observableConfigRef(nil), g.observableConfigs...)
}

func (g RuntimeResourceGraph) HooksConfig() hooks.Config {
	return cloneHooksConfig(g.hooks)
}

func (g RuntimeResourceGraph) Nodes() []RuntimeResourceNode {
	return append([]RuntimeResourceNode(nil), g.nodes...)
}

func runtimeResourceNodes(paths config.ResourcePaths, extResources extensions.Resources, runtimeContexts map[string]ExtensionRuntimeContext) []RuntimeResourceNode {
	var nodes []RuntimeResourceNode
	if paths.UserAgentsResources && paths.HomeAgentsDir != "" {
		nodes = append(nodes,
			runtimeResourceNode(RuntimeResourceSkillDir, "user", filepath.Join(paths.HomeAgentsDir, "skills"), false, false),
			runtimeResourceNode(RuntimeResourceMCPConfig, "user", filepath.Join(paths.HomeAgentsDir, "mcp.json"), false, false),
		)
	}
	skillDirsByExt := resourceRefsByExtension(extResources.SkillDirs)
	mcpConfigsByExt := resourceRefsByExtension(extResources.MCPConfigs)
	hookFilesByExt := resourceRefsByExtension(extResources.HookFiles)
	observableConfigsByExt := resourceRefsByExtension(extResources.ObservableConfigs)
	for _, ext := range extResources.Extensions {
		runtimeContext := runtimeContexts[ext.Name]
		nodes = append(nodes, RuntimeResourceNode{
			Kind:             RuntimeResourceExtension,
			Source:           ext.Source,
			Path:             ext.Dir,
			ExtensionName:    ext.Name,
			ExtensionDir:     ext.Dir,
			ExtensionDataDir: runtimeContext.DataDir,
			RequireTrust:     ext.RequireTrust,
			Precedence:       runtimeSourceRank(ext.Source),
		})
		for _, ref := range skillDirsByExt[ext.Name] {
			nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceSkillDir, ref, runtimeContexts[ref.ExtensionName], true))
		}
		for _, ref := range mcpConfigsByExt[ext.Name] {
			nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceMCPConfig, ref, runtimeContexts[ref.ExtensionName], true))
		}
		for _, ref := range hookFilesByExt[ext.Name] {
			nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceHookFile, ref, runtimeContexts[ref.ExtensionName], true))
		}
		for _, ref := range observableConfigsByExt[ext.Name] {
			nodes = append(nodes, runtimeExtensionResourceNode(RuntimeResourceObservableConfig, ref, runtimeContexts[ref.ExtensionName], true))
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

func runtimeExtensionDescriptors(selected []extensions.Extension, runtimeContexts map[string]ExtensionRuntimeContext) []RuntimeExtensionDescriptor {
	descriptors := make([]RuntimeExtensionDescriptor, 0, len(selected))
	for _, ext := range selected {
		descriptors = append(descriptors, RuntimeExtensionDescriptor{
			Name:         ext.Name,
			Dir:          ext.Dir,
			Source:       ext.Source,
			Scope:        ext.Scope,
			RequireTrust: ext.RequireTrust,
			Manifest:     ext.Manifest,
			Runtime:      runtimeContexts[ext.Name],
		})
	}
	return descriptors
}

func resourceRefsByExtension(refs []extensions.ResourceRef) map[string][]extensions.ResourceRef {
	byExtension := make(map[string][]extensions.ResourceRef)
	for _, ref := range refs {
		byExtension[ref.ExtensionName] = append(byExtension[ref.ExtensionName], ref)
	}
	return byExtension
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

func runtimeExtensionResourceNode(kind RuntimeResourceKind, ref extensions.ResourceRef, runtimeContext ExtensionRuntimeContext, strictConflicts bool) RuntimeResourceNode {
	node := runtimeResourceNode(kind, ref.Source, ref.Path, ref.RequireTrust, strictConflicts)
	node.ExtensionName = ref.ExtensionName
	node.ExtensionDir = ref.ExtensionDir
	node.ExtensionDataDir = runtimeContext.DataDir
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
	if paths.UserAgentsResources && paths.HomeAgentsDir != "" {
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

func mcpConfigRefs(paths config.ResourcePaths, extRefs []extensions.ResourceRef, runtimeContexts map[string]ExtensionRuntimeContext) []mcpConfigRef {
	var refs []mcpConfigRef
	if paths.UserAgentsResources && paths.HomeAgentsDir != "" {
		refs = append(refs, mcpConfigRef{
			Path:   filepath.Join(paths.HomeAgentsDir, "mcp.json"),
			Source: "user",
		})
	}
	for _, ref := range extRefs {
		refs = append(refs, mcpConfigRef{
			Path:             ref.Path,
			Source:           ref.Source,
			ExtensionRuntime: runtimeContexts[ref.ExtensionName],
			StrictConflicts:  true,
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

func observableConfigRefs(extRefs []extensions.ResourceRef, runtimeContexts map[string]ExtensionRuntimeContext) []observableConfigRef {
	refs := make([]observableConfigRef, 0, len(extRefs))
	for _, ref := range extRefs {
		refs = append(refs, observableConfigRef{
			Path:             ref.Path,
			Source:           ref.Source,
			ExtensionRuntime: runtimeContexts[ref.ExtensionName],
		})
	}
	return refs
}

func observableReadOnlyConfigSources(refs []observableConfigRef) []observable.ReadOnlyConfigSource {
	out := make([]observable.ReadOnlyConfigSource, 0, len(refs))
	for _, ref := range refs {
		runtimeContext := ref.ExtensionRuntime
		out = append(out, observable.ReadOnlyConfigSource{
			Path:   ref.Path,
			Source: ref.Source,
			Runtime: observable.RuntimeContext{
				ExtensionDir:            runtimeContext.ExtensionDir,
				ExtensionDataDir:        runtimeContext.DataDir,
				PrepareExtensionDataDir: runtimeContext.PrepareDataDir,
			},
		})
	}
	return out
}

func extensionRuntimeContexts(cfg config.Config, selected []extensions.Extension) map[string]ExtensionRuntimeContext {
	contexts := make(map[string]ExtensionRuntimeContext, len(selected))
	for _, extension := range selected {
		contexts[extension.Name] = newExtensionRuntimeContext(cfg.AgentAddress, extension)
	}
	return contexts
}

func appendExtensionHooks(base hooks.Config, refs []extensions.ResourceRef, runtimeContexts map[string]ExtensionRuntimeContext) (hooks.Config, error) {
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
		runtimeContext := runtimeContexts[ref.ExtensionName]
		for _, command := range cfg.Commands {
			command.Runtime = hooks.RuntimeContext{
				ExtensionDir:            runtimeContext.ExtensionDir,
				ExtensionDataDir:        runtimeContext.DataDir,
				PrepareExtensionDataDir: runtimeContext.PrepareDataDir,
			}
			if prev, ok := names[command.Name]; ok {
				return hooks.Config{}, fmt.Errorf("extensions: duplicate hook %q from %s and %s", command.Name, prev, command.Source)
			}
			names[command.Name] = command.Source
			out.Commands = append(out.Commands, command)
		}
	}
	return out, nil
}

func loadMCPConfigRefs(refs []mcpConfigRef, workDir string, runtimeEnvironment environment.Snapshot) ([]mcp.Config, mcp.Config, map[string]string, error) {
	return loadMCPConfigRefsWithOptions(refs, workDir, runtimeEnvironment, mcpConfigLoadOptions{})
}

type mcpConfigLoadOptions struct {
	EnableExtensionData bool
}

func loadMCPConfigRefsForRuntime(refs []mcpConfigRef, workDir string, runtimeEnvironment environment.Snapshot) ([]mcp.Config, mcp.Config, map[string]string, error) {
	return loadMCPConfigRefsWithOptions(refs, workDir, runtimeEnvironment, mcpConfigLoadOptions{
		EnableExtensionData: true,
	})
}

func loadMCPConfigRefsWithOptions(refs []mcpConfigRef, workDir string, runtimeEnvironment environment.Snapshot, opts mcpConfigLoadOptions) ([]mcp.Config, mcp.Config, map[string]string, error) {
	type loadedConfig struct {
		ref mcpConfigRef
		cfg mcp.Config
	}
	loaded := make([]loadedConfig, 0, len(refs))
	sources := map[string]string{}
	strict := map[string]bool{}
	winnerLayer := map[string]int{}
	for layer, ref := range refs {
		cfg, err := mcp.LoadConfig(ref.Path)
		if err != nil {
			return nil, mcp.Config{}, nil, err
		}
		for name := range cfg.MCPServers {
			if prevSource, ok := sources[name]; ok && (strict[name] || ref.StrictConflicts) {
				return nil, mcp.Config{}, nil, fmt.Errorf("extensions: duplicate MCP server %q from %s and %s", name, prevSource, ref.Source)
			}
			sources[name] = ref.Source
			strict[name] = ref.StrictConflicts
			winnerLayer[name] = layer
		}
		loaded = append(loaded, loadedConfig{ref: ref, cfg: cfg})
	}

	var configs []mcp.Config
	merged := mcp.Config{MCPServers: map[string]mcp.ServerSpec{}, Sources: map[string]string{}}
	for layer, item := range loaded {
		effective := mcp.Config{MCPServers: map[string]mcp.ServerSpec{}, Sources: map[string]string{}}
		for name, spec := range item.cfg.MCPServers {
			if winnerLayer[name] == layer {
				effective.MCPServers[name] = spec
				effective.Sources[name] = item.ref.Source
			}
		}
		if len(effective.MCPServers) == 0 {
			continue
		}
		extensionDataDir := ""
		var prepareLocalProcess func() error
		if opts.EnableExtensionData && effective.HasLocalServers() && item.ref.ExtensionRuntime.DataDir != "" {
			extensionDataDir = item.ref.ExtensionRuntime.DataDir
			prepareLocalProcess = item.ref.ExtensionRuntime.PrepareDataDir
		}
		prepared, err := mcp.PrepareConfigWithOptions(effective, mcp.PrepareOptions{
			WorkDir:             workDir,
			ExtensionDir:        item.ref.ExtensionRuntime.ExtensionDir,
			ExtensionDataDir:    extensionDataDir,
			PrepareLocalProcess: prepareLocalProcess,
			Environment:         runtimeEnvironment,
		})
		if err != nil {
			return nil, mcp.Config{}, nil, err
		}
		for name, spec := range prepared.MCPServers {
			merged.MCPServers[name] = spec
			merged.Sources[name] = prepared.Sources[name]
		}
		configs = append(configs, prepared)
	}
	return configs, merged, sources, nil
}

// LoadMCPConfigs prepares process-scoped MCP configs from the same
// immutable Agent runtime resolution used by Threads and Runtime status.
func LoadMCPConfigs(runtime AgentRuntimeResolution, workDir string) ([]mcp.Config, error) {
	configs, _, _, err := loadMCPConfigRefsForRuntime(
		runtime.ResourceGraph().MCPConfigs(),
		workDir,
		runtime.Environment(),
	)
	return configs, err
}
