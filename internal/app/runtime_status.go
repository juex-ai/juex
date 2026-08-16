package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/modules/builtintools"
	skillsmodule "github.com/juex-ai/juex/internal/modules/skills"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/prompt"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

// RuntimeCatalogService assembles read-only runtime facts for presentation
// layers such as the web UI.
type RuntimeCatalogService struct {
	cfg config.Config
}

func NewRuntimeCatalogService(cfg config.Config) RuntimeCatalogService {
	return RuntimeCatalogService{cfg: cfg}
}

type RuntimeStatusOptions struct {
	MCPToolDescriptors map[string][]mcp.ToolDescriptor
	MCPErrors          map[string]string
	MCPConnectionSpecs map[string]mcp.RuntimeConnectionSpec
	SkillCache         *RuntimeStatusSkillCache
	ScratchpadDir      string
	AgentRuntime       *AgentRuntimeResolution
}

// RuntimeStatusSkillCache caches loaded skills for repeated status snapshots.
// The cache is keyed by the resolved skill directory list, so tests and callers
// that swap config directories get a fresh load without leaking skills.Loader
// details into presentation layers.
type RuntimeStatusSkillCache struct {
	mu     sync.Mutex
	dirs   []skills.Dir
	policy config.SkillPolicy
	status RuntimeSkillsStatus
	loader *skills.Loader
	loaded bool
}

func NewRuntimeStatusSkillCache() *RuntimeStatusSkillCache {
	return &RuntimeStatusSkillCache{}
}

type RuntimeStatus struct {
	WorkDir      string
	Provider     RuntimeProviderStatus
	Shell        config.ShellProfile
	Sandbox      sandbox.Policy
	Extensions   RuntimeExtensionsStatus
	SystemPrompt RuntimeSystemPromptStatus
	Tools        RuntimeToolsStatus
	MCP          RuntimeMCPStatus
	Hooks        RuntimeHooksStatus
	Skills       RuntimeSkillsStatus
}

type RuntimeExtensionsStatus struct {
	Count int
	Items []RuntimeExtensionStatus
}

type RuntimeExtensionStatus struct {
	ManifestVersion int
	Name            string
	Version         string
	Description     string
	DisplayName     string
	Author          string
	Homepage        string
	Repository      string
	License         string
	Requirements    []RuntimeExtensionRequirement
	Scope           string
	Path            string
	Resources       RuntimeExtensionResourceCounts
	Environment     []RuntimeExtensionEnvironmentDeclaration
}

type RuntimeExtensionRequirement struct {
	Name        string
	Description string
	URL         string
}

type RuntimeExtensionResourceCounts struct {
	Skills      int
	MCPServers  int
	Hooks       int
	Observables int
}

type RuntimeToolsStatus struct {
	Count  int
	Groups []RuntimeToolGroupStatus
}

type RuntimeToolGroupStatus struct {
	Group string
	Tools []RuntimeToolInfo
}

type RuntimeToolInfo struct {
	Name        string
	Module      string
	Description string
	Schema      map[string]any
	Timeout     RuntimeToolTimeout
}

type RuntimeToolTimeout struct {
	Mode    string
	Seconds int
}

type RuntimeProviderStatus struct {
	ID           string
	Protocol     string
	Model        string
	BaseURL      string
	Capabilities llm.ProviderCapabilities
}

type RuntimeSystemPromptStatus struct {
	Count int
	Items []RuntimeSystemPromptEntry
}

type RuntimeSystemPromptEntry struct {
	Key    string
	Label  string
	Source string
	Path   string
	Tokens int
	Text   string
}

type RuntimeMCPStatus struct {
	Configured int
	Connected  int
	Errors     int
	Servers    []RuntimeMCPServerStatus
}

type RuntimeMCPServerStatus struct {
	Name      string
	Source    string
	Type      string
	URL       string
	Command   string
	Args      []string
	Status    string
	Connected bool
	ToolCount int
	Tools     []RuntimeToolInfo
	Error     string
}

type RuntimeHooksStatus struct {
	Configured int
	Commands   []RuntimeHookInfo
}

type RuntimeHookInfo struct {
	Name           string
	Source         string
	Events         []string
	Tools          []string
	Command        []string
	Required       bool
	TimeoutSeconds int
	MaxOutputBytes int
}

type RuntimeSkillsStatus struct {
	Count    int
	Items    []RuntimeSkillInfo
	Filtered []RuntimeSkillFilteredInfo
	Prompt   RuntimeSkillPromptStatus
}

type RuntimeSkillInfo struct {
	Name        string
	Description string
	Type        string
	Source      string
	Path        string
}

type RuntimeSkillFilteredInfo struct {
	Name   string
	Source string
	Reason string
}

type RuntimeSkillPromptStatus struct {
	BudgetChars int
	UsedChars   int
	Compacted   bool
	Omitted     []RuntimeSkillOmittedInfo
}

type RuntimeSkillOmittedInfo struct {
	Name   string
	Source string
	Reason string
}

func (s RuntimeCatalogService) Snapshot(opts RuntimeStatusOptions) (RuntimeStatus, error) {
	var agentRuntime AgentRuntimeResolution
	if opts.AgentRuntime != nil {
		agentRuntime = *opts.AgentRuntime
	} else {
		var err error
		agentRuntime, err = ResolveAgentRuntime(s.cfg)
		if err != nil {
			return RuntimeStatus{}, err
		}
	}
	resourceGraph := agentRuntime.ResourceGraph()
	skillStatus, skillLoader, err := s.skillsStatus(opts.SkillCache, resourceGraph.SkillDirs(), s.cfg.SkillPolicy())
	if err != nil {
		return RuntimeStatus{}, err
	}
	systemPrompt, err := s.systemPromptStatus(skillLoader, opts.ScratchpadDir)
	if err != nil {
		return RuntimeStatus{}, err
	}
	mcpStatus, err := s.mcpStatus(opts, resourceGraph.MCPConfigs(), agentRuntime.Environment())
	if err != nil {
		return RuntimeStatus{}, err
	}
	toolsStatus, err := s.toolsStatus(skillLoader)
	if err != nil {
		return RuntimeStatus{}, err
	}
	hookStatus := hooksStatus(resourceGraph.HooksConfig())
	extensionsStatus, err := runtimeExtensionsStatus(resourceGraph, skillStatus, mcpStatus, hookStatus, agentRuntime.EnvironmentDeclarations())
	if err != nil {
		return RuntimeStatus{}, err
	}
	return RuntimeStatus{
		WorkDir:      s.absoluteWorkDir(),
		Provider:     providerRuntimeStatusFromConfig(s.cfg),
		Shell:        s.cfg.Shell,
		Sandbox:      s.cfg.SandboxPolicy(),
		Extensions:   extensionsStatus,
		SystemPrompt: systemPrompt,
		Tools:        toolsStatus,
		MCP:          mcpStatus,
		Hooks:        hookStatus,
		Skills:       skillStatus,
	}, nil
}

func runtimeExtensionsStatus(graph RuntimeResourceGraph, skills RuntimeSkillsStatus, mcpStatus RuntimeMCPStatus, hookStatus RuntimeHooksStatus, environmentDeclarations []RuntimeExtensionEnvironmentDeclaration) (RuntimeExtensionsStatus, error) {
	descriptors := graph.Extensions()
	items := make([]RuntimeExtensionStatus, 0, len(descriptors))
	indexes := make(map[string]int, len(descriptors))
	for _, descriptor := range descriptors {
		manifest := descriptor.Manifest
		requirements := make([]RuntimeExtensionRequirement, 0, len(manifest.Requirements))
		for _, requirement := range manifest.Requirements {
			requirements = append(requirements, RuntimeExtensionRequirement{
				Name: requirement.Name, Description: requirement.Description, URL: requirement.URL,
			})
		}
		indexes[descriptor.Source] = len(items)
		items = append(items, RuntimeExtensionStatus{
			ManifestVersion: manifest.ManifestVersion,
			Name:            descriptor.Name,
			Version:         manifest.Version,
			Description:     manifest.Description,
			DisplayName:     manifest.DisplayName,
			Author:          manifest.Author,
			Homepage:        manifest.Homepage,
			Repository:      manifest.Repository,
			License:         manifest.License,
			Requirements:    requirements,
			Scope:           string(descriptor.Scope),
			Path:            descriptor.Dir,
		})
	}
	for _, declaration := range environmentDeclarations {
		if index, ok := indexes[declaration.Source]; ok {
			items[index].Environment = append(items[index].Environment, declaration)
		}
	}
	for _, skill := range skills.Items {
		if index, ok := indexes[skill.Source]; ok {
			items[index].Resources.Skills++
		}
	}
	for _, server := range mcpStatus.Servers {
		if index, ok := indexes[server.Source]; ok {
			items[index].Resources.MCPServers++
		}
	}
	for _, hook := range hookStatus.Commands {
		if index, ok := indexes[hook.Source]; ok {
			items[index].Resources.Hooks++
		}
	}
	for _, ref := range graph.ObservableConfigs() {
		index, ok := indexes[ref.Source]
		if !ok {
			continue
		}
		cfg, issues, err := observable.LoadConfigLenient(ref.Path)
		if err != nil {
			return RuntimeExtensionsStatus{}, err
		}
		items[index].Resources.Observables += len(cfg.Observables) + len(issues)
	}
	return RuntimeExtensionsStatus{Count: len(items), Items: items}, nil
}

func (s RuntimeCatalogService) toolsStatus(skillLoader *skills.Loader) (status RuntimeToolsStatus, err error) {
	ctx := context.Background()
	runtimeContext := runtimemodule.RuntimeContext{WorkDir: s.cfg.WorkDir}
	builtin := builtintools.New(ctx, tools.BuiltinOptions{
		WorkDir:            s.cfg.WorkDir,
		Shell:              toolsShellProfile(s.cfg.Shell),
		ToolTimeoutSeconds: durationSeconds(s.cfg.RuntimeLimits().ToolTimeout),
	})
	runtimeSet, err := runtimemodule.BuildRuntimeSet(ctx, []runtimemodule.RuntimeFactorySpec{
		{ID: builtintools.ModuleID, Enabled: true, New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return builtin, nil
		}},
		{ID: skillsmodule.ModuleID, Enabled: true, New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return skillsmodule.NewWithLoader(skillLoader, s.cfg.WorkDir, s.cfg.SandboxPolicy()), nil
		}},
		{ID: sideSessionModuleID, Enabled: true, New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return &sideSessionModule{}, nil
		}},
		{ID: observable.ModuleID, Enabled: true, New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return observable.NewModule(nil), nil
		}},
	}, runtimeContext, runtimemodule.ToolContext{Runtime: runtimeContext})
	if err != nil {
		_ = builtin.CloseRuntime(context.Background())
		return RuntimeToolsStatus{}, err
	}
	if err := runtimeSet.StartRuntime(ctx, runtimeContext); err != nil {
		return RuntimeToolsStatus{}, err
	}
	defer func() {
		err = errors.Join(err, runtimeSet.CloseRuntime(context.Background()))
	}()
	sessionContext := runtimemodule.SessionContext{}
	sessionSet, err := runtimemodule.BuildSessionSet(ctx, []runtimemodule.SessionFactorySpec{
		{ID: juexruntime.GoalModuleID, Enabled: true, New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
			return juexruntime.NewGoalModule(nil), nil
		}},
		{ID: juexruntime.NotesModuleID, Enabled: true, New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
			return juexruntime.NewNotesModule(nil), nil
		}},
	}, sessionContext, runtimemodule.ToolContext{Runtime: runtimeContext, Session: &sessionContext})
	if err != nil {
		return RuntimeToolsStatus{}, err
	}
	if _, err := runtimemodule.BuildToolRegistry(tools.RegistryOptions{}, runtimeSet, sessionSet); err != nil {
		return RuntimeToolsStatus{}, err
	}
	return runtimeToolsStatusFromCatalogs(durationSeconds(s.cfg.RuntimeLimits().ToolTimeout), runtimeSet.ToolCatalog(), sessionSet.ToolCatalog())
}

func runtimeToolsStatusFromCatalogs(defaultTimeoutSeconds int, catalogs ...runtimemodule.ToolCatalog) (RuntimeToolsStatus, error) {
	var entries []runtimemodule.ToolEntry
	for _, catalog := range catalogs {
		entries = append(entries, catalog.Entries()...)
	}
	definitions := make([]tools.ToolDefinition, 0, len(entries))
	owners := make(map[string]string, len(entries))
	for _, entry := range entries {
		definition := entry.Tool.Definition()
		definitions = append(definitions, definition)
		owners[definition.Name] = string(entry.ModuleID)
	}
	status, err := runtimeToolsStatusFromDefinitions(definitions, defaultTimeoutSeconds)
	if err != nil {
		return RuntimeToolsStatus{}, err
	}
	for groupIndex := range status.Groups {
		for toolIndex := range status.Groups[groupIndex].Tools {
			status.Groups[groupIndex].Tools[toolIndex].Module = owners[status.Groups[groupIndex].Tools[toolIndex].Name]
		}
	}
	return status, nil
}

func runtimeToolsStatusFromDefinitions(definitions []tools.ToolDefinition, defaultTimeoutSeconds int) (RuntimeToolsStatus, error) {
	groupOrder := []tools.ToolGroup{
		tools.ToolGroupFile,
		tools.ToolGroupChunkedWrite,
		tools.ToolGroupShell,
		tools.ToolGroupSearch,
		tools.ToolGroupSkill,
		tools.ToolGroupSessionState,
		tools.ToolGroupSideSession,
		tools.ToolGroupObservable,
	}
	groups := make([]RuntimeToolGroupStatus, len(groupOrder))
	groupIndexes := make(map[tools.ToolGroup]int, len(groupOrder))
	for i, group := range groupOrder {
		groups[i] = RuntimeToolGroupStatus{Group: string(group), Tools: []RuntimeToolInfo{}}
		groupIndexes[group] = i
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		groupIndex, ok := groupIndexes[definition.Group]
		if !ok {
			return RuntimeToolsStatus{}, fmt.Errorf("runtime tools: tool %q has invalid builtin group %q", definition.Name, definition.Group)
		}
		if _, exists := seen[definition.Name]; exists {
			return RuntimeToolsStatus{}, fmt.Errorf("runtime tools: duplicate tool %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		groups[groupIndex].Tools = append(groups[groupIndex].Tools, runtimeToolInfoFromDefinition(definition, defaultTimeoutSeconds))
	}
	for i := range groups {
		sort.Slice(groups[i].Tools, func(left, right int) bool {
			return groups[i].Tools[left].Name < groups[i].Tools[right].Name
		})
	}
	return RuntimeToolsStatus{Count: len(definitions), Groups: groups}, nil
}

func runtimeToolInfoFromDefinition(definition tools.ToolDefinition, defaultTimeoutSeconds int) RuntimeToolInfo {
	definition = definition.Normalized()
	effective := tools.EffectiveToolTimeout(definition, defaultTimeoutSeconds)
	return RuntimeToolInfo{
		Name:        definition.Name,
		Description: definition.Description,
		Schema:      definition.Schema,
		Timeout: RuntimeToolTimeout{
			Mode:    string(effective.Mode),
			Seconds: effective.Seconds,
		},
	}
}

func hooksStatus(cfg hooks.Config) RuntimeHooksStatus {
	commands := make([]RuntimeHookInfo, 0, len(cfg.Commands))
	for _, command := range cfg.Commands {
		events := make([]string, 0, len(command.Events))
		for _, event := range command.Events {
			events = append(events, string(event))
		}
		timeoutSeconds := command.TimeoutSeconds
		if timeoutSeconds <= 0 {
			timeoutSeconds = hooks.DefaultTimeoutSeconds
		}
		maxOutputBytes := command.MaxOutputBytes
		if maxOutputBytes <= 0 {
			maxOutputBytes = hooks.DefaultMaxOutputBytes
		}
		commands = append(commands, RuntimeHookInfo{
			Name:           command.Name,
			Source:         command.Source,
			Events:         events,
			Tools:          append([]string(nil), command.Tools...),
			Command:        append([]string(nil), command.Command...),
			Required:       command.Required,
			TimeoutSeconds: timeoutSeconds,
			MaxOutputBytes: maxOutputBytes,
		})
	}
	return RuntimeHooksStatus{Configured: len(commands), Commands: commands}
}

func (s RuntimeCatalogService) systemPromptStatus(skillLoader *skills.Loader, scratchpadDir string) (RuntimeSystemPromptStatus, error) {
	skillModule := skillsmodule.NewWithLoader(skillLoader, s.cfg.WorkDir, s.cfg.SandboxPolicy())
	runtimeContext := runtimemodule.RuntimeContext{WorkDir: s.cfg.WorkDir}
	runtimeSet, err := runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{
		{
			ID:      prompt.GuidanceModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return &prompt.GuidanceModule{GlobalAgentsMDPath: s.cfg.GlobalAgentsMDPath(), AgentsMDDirs: s.cfg.AgentsMDDirs()}, nil
			},
		},
		{
			ID:      skillsmodule.ModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return skillModule, nil
			},
		},
	}, runtimeContext, runtimemodule.ToolContext{Runtime: runtimeContext})
	if err != nil {
		return RuntimeSystemPromptStatus{}, err
	}
	sessionContext := runtimemodule.SessionContext{ScratchpadDir: scratchpadDir}
	sessionSet, err := runtimemodule.BuildSessionSet(context.Background(), []runtimemodule.SessionFactorySpec{{
		ID:      prompt.SessionContextModuleID,
		Enabled: true,
		New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
			return &prompt.SessionContextModule{WorkDir: s.cfg.WorkDir, Shell: prompt.ShellProfileFromConfig(s.cfg.Shell)}, nil
		},
	}}, sessionContext, runtimemodule.ToolContext{Runtime: runtimeContext, Session: &sessionContext})
	if err != nil {
		return RuntimeSystemPromptStatus{}, err
	}
	builder := &prompt.Builder{
		ModulePromptContext: func() ([]runtimemodule.PromptSection, error) {
			return runtimemodule.CollectContext(context.Background(), runtimemodule.ContextRequest{
				Purpose: runtimemodule.ContextPurposeProviderIteration,
				Runtime: runtimeContext,
				Session: &sessionContext,
			}, runtimeSet, sessionSet)
		},
	}
	sections, err := builder.SectionsWithError()
	if err != nil {
		return RuntimeSystemPromptStatus{}, err
	}
	items := make([]RuntimeSystemPromptEntry, 0, len(sections))
	for _, section := range sections {
		items = append(items, RuntimeSystemPromptEntry{
			Key:    section.Key,
			Label:  runtimePromptLabel(section),
			Source: runtimePromptSource(section),
			Path:   section.Path,
			Tokens: juexruntime.EstimateTextTokens(section.Text),
			Text:   section.Text,
		})
	}
	return RuntimeSystemPromptStatus{Count: len(items), Items: items}, nil
}

func runtimePromptLabel(section prompt.Section) string {
	if section.Label != "" {
		return section.Label
	}
	return section.Key
}

func runtimePromptSource(section prompt.Section) string {
	if section.Source != "" {
		return section.Source
	}
	return "runtime"
}

func (s RuntimeCatalogService) skillsStatus(cache *RuntimeStatusSkillCache, dirs []skills.Dir, policy config.SkillPolicy) (RuntimeSkillsStatus, *skills.Loader, error) {
	if cache != nil {
		return cache.snapshot(dirs, policy)
	}
	return loadRuntimeSkills(dirs, policy)
}

func (c *RuntimeStatusSkillCache) snapshot(dirs []skills.Dir, policy config.SkillPolicy) (RuntimeSkillsStatus, *skills.Loader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && skillDirsEqual(c.dirs, dirs) && skillPoliciesEqual(c.policy, policy) {
		return cloneRuntimeSkillsStatus(c.status), c.loader, nil
	}
	status, loader, err := loadRuntimeSkills(dirs, policy)
	if err != nil {
		return RuntimeSkillsStatus{}, nil, err
	}
	c.dirs = append([]skills.Dir(nil), dirs...)
	c.policy = cloneSkillPolicy(policy)
	c.status = cloneRuntimeSkillsStatus(status)
	c.loader = loader
	c.loaded = true
	return status, loader, nil
}

func loadRuntimeSkills(dirs []skills.Dir, policy config.SkillPolicy) (RuntimeSkillsStatus, *skills.Loader, error) {
	skillLoader := skills.NewLoaderFromDirsWithOptions(dirs, skills.LoaderOptions{Policy: skills.Policy{
		Include:           policy.Include,
		Exclude:           policy.Exclude,
		PromptBudgetChars: policy.PromptBudgetChars,
	}})
	if err := skillLoader.Load(); err != nil {
		return RuntimeSkillsStatus{}, nil, err
	}
	loadedSkills := skillLoader.All()
	items := make([]RuntimeSkillInfo, 0, len(loadedSkills))
	for _, skill := range loadedSkills {
		items = append(items, RuntimeSkillInfo{
			Name:        skill.Name,
			Description: skill.Description,
			Type:        skill.Type,
			Source:      skill.Source,
			Path:        skill.Path,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return runtimeSourceLess(items[i].Source, items[i].Name, items[j].Source, items[j].Name)
	})
	filtered := make([]RuntimeSkillFilteredInfo, 0, len(skillLoader.Filtered()))
	for _, item := range skillLoader.Filtered() {
		filtered = append(filtered, RuntimeSkillFilteredInfo{Name: item.Name, Source: item.Source, Reason: item.Reason})
	}
	report := skillLoader.PromptReport()
	omitted := make([]RuntimeSkillOmittedInfo, 0, len(report.Omitted))
	for _, item := range report.Omitted {
		omitted = append(omitted, RuntimeSkillOmittedInfo{Name: item.Name, Source: item.Source, Reason: item.Reason})
	}
	return RuntimeSkillsStatus{
		Count:    len(items),
		Items:    items,
		Filtered: filtered,
		Prompt: RuntimeSkillPromptStatus{
			BudgetChars: report.BudgetChars,
			UsedChars:   report.UsedChars,
			Compacted:   report.Compacted,
			Omitted:     omitted,
		},
	}, skillLoader, nil
}

func cloneRuntimeSkillsStatus(status RuntimeSkillsStatus) RuntimeSkillsStatus {
	status.Items = append([]RuntimeSkillInfo(nil), status.Items...)
	status.Filtered = append([]RuntimeSkillFilteredInfo(nil), status.Filtered...)
	status.Prompt.Omitted = append([]RuntimeSkillOmittedInfo(nil), status.Prompt.Omitted...)
	return status
}

func skillDirsEqual(left, right []skills.Dir) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func skillPoliciesEqual(left, right config.SkillPolicy) bool {
	if left.PromptBudgetChars != right.PromptBudgetChars {
		return false
	}
	if len(left.Include) != len(right.Include) || len(left.Exclude) != len(right.Exclude) {
		return false
	}
	for i := range left.Include {
		if left.Include[i] != right.Include[i] {
			return false
		}
	}
	for i := range left.Exclude {
		if left.Exclude[i] != right.Exclude[i] {
			return false
		}
	}
	return true
}

func cloneSkillPolicy(policy config.SkillPolicy) config.SkillPolicy {
	policy.Include = append([]string(nil), policy.Include...)
	policy.Exclude = append([]string(nil), policy.Exclude...)
	return policy
}

type runtimeMCPServerConfig struct {
	Name   string
	Source string
	Spec   mcp.ServerSpec
}

func (s RuntimeCatalogService) mcpStatus(opts RuntimeStatusOptions, refs []mcpConfigRef, runtimeEnvironment environment.Snapshot) (RuntimeMCPStatus, error) {
	var servers []runtimeMCPServerConfig
	if opts.MCPConnectionSpecs != nil {
		servers = runtimeMCPServersFromConnectionSpecs(opts.MCPConnectionSpecs)
	} else {
		var err error
		servers, err = s.configuredMCPServers(refs, runtimeEnvironment)
		if err != nil {
			return RuntimeMCPStatus{}, err
		}
	}
	connectedCount := 0
	errorCount := 0
	statuses := make([]RuntimeMCPServerStatus, 0, len(servers))
	defaultTimeoutSeconds := durationSeconds(s.cfg.RuntimeLimits().ToolTimeout)
	for _, server := range servers {
		transport, err := server.Spec.NormalizedTransport()
		if err != nil {
			return RuntimeMCPStatus{}, fmt.Errorf("mcp server %q transport: %w", server.Name, err)
		}
		displayURL, err := server.Spec.DisplayURL()
		if err != nil {
			return RuntimeMCPStatus{}, fmt.Errorf("mcp server %q display url: %w", server.Name, err)
		}
		descriptors, connected := opts.MCPToolDescriptors[server.Name]
		errText := opts.MCPErrors[server.Name]
		errText, err = server.Spec.DisplaySafeText(errText)
		if err != nil {
			return RuntimeMCPStatus{}, fmt.Errorf("mcp server %q display error: %w", server.Name, err)
		}
		status := "not_started"
		projectedTools := []RuntimeToolInfo{}
		if errText != "" {
			status = "error"
			connected = false
		} else if connected {
			status = "connected"
			projectedTools, err = runtimeMCPToolInfos(server.Name, descriptors, defaultTimeoutSeconds)
			if err != nil {
				return RuntimeMCPStatus{}, err
			}
		}
		info := RuntimeMCPServerStatus{
			Name:      server.Name,
			Source:    server.Source,
			Type:      transport,
			URL:       displayURL,
			Command:   server.Spec.Command,
			Args:      append([]string(nil), server.Spec.Args...),
			Status:    status,
			Connected: connected,
			ToolCount: len(projectedTools),
			Tools:     projectedTools,
			Error:     errText,
		}
		if info.Connected {
			connectedCount++
		} else if info.Status == "error" {
			errorCount++
		}
		statuses = append(statuses, info)
	}
	return RuntimeMCPStatus{
		Configured: len(statuses),
		Connected:  connectedCount,
		Errors:     errorCount,
		Servers:    statuses,
	}, nil
}

func runtimeMCPServersFromConnectionSpecs(specs map[string]mcp.RuntimeConnectionSpec) []runtimeMCPServerConfig {
	servers := make([]runtimeMCPServerConfig, 0, len(specs))
	for name, connection := range specs {
		servers = append(servers, runtimeMCPServerConfig{
			Name:   name,
			Source: connection.Source,
			Spec:   connection.Spec,
		})
	}
	sort.Slice(servers, func(i, j int) bool {
		return runtimeSourceLess(servers[i].Source, servers[i].Name, servers[j].Source, servers[j].Name)
	})
	return servers
}

func runtimeMCPToolInfos(serverName string, descriptors []mcp.ToolDescriptor, defaultTimeoutSeconds int) ([]RuntimeToolInfo, error) {
	module := mcp.NewDescriptorModule(map[string][]mcp.ToolDescriptor{serverName: descriptors})
	set, err := runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
		ID:      mcp.ModuleID,
		Enabled: true,
		New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return module, nil
		},
	}}, runtimemodule.RuntimeContext{}, runtimemodule.ToolContext{})
	if err != nil {
		return nil, err
	}
	byName := make(map[string]runtimemodule.ToolEntry, len(descriptors))
	for _, entry := range set.ToolCatalog().Entries() {
		byName[entry.Tool.Name] = entry
	}
	infos := make([]RuntimeToolInfo, 0, len(descriptors))
	for _, descriptor := range descriptors {
		entry, ok := byName[mcp.ToolName(serverName, descriptor.Name)]
		if !ok {
			return nil, fmt.Errorf("mcp server %q tool %q missing from Module catalog", serverName, descriptor.Name)
		}
		info := runtimeToolInfoFromDefinition(entry.Tool.Definition(), defaultTimeoutSeconds)
		info.Name = descriptor.Name
		info.Module = string(entry.ModuleID)
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

func (s RuntimeCatalogService) configuredMCPServers(refs []mcpConfigRef, runtimeEnvironment environment.Snapshot) ([]runtimeMCPServerConfig, error) {
	serversByName := map[string]runtimeMCPServerConfig{}
	_, merged, sources, err := loadMCPConfigRefs(refs, s.absoluteWorkDir(), runtimeEnvironment)
	if err != nil {
		return nil, err
	}
	for name, spec := range merged.MCPServers {
		serversByName[name] = runtimeMCPServerConfig{
			Name:   name,
			Source: sources[name],
			Spec:   spec,
		}
	}
	servers := make([]runtimeMCPServerConfig, 0, len(serversByName))
	for _, server := range serversByName {
		servers = append(servers, server)
	}
	sort.Slice(servers, func(i, j int) bool {
		return runtimeSourceLess(servers[i].Source, servers[i].Name, servers[j].Source, servers[j].Name)
	})
	return servers, nil
}

func (s RuntimeCatalogService) absoluteWorkDir() string {
	workDir := s.cfg.WorkDir
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		workDir = cwd
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return workDir
	}
	return abs
}

func providerRuntimeStatusFromConfig(cfg config.Config) RuntimeProviderStatus {
	if cfg.ProviderID == "" && cfg.ProviderProtocol == "" {
		return RuntimeProviderStatus{Model: cfg.Model, BaseURL: cfg.BaseURL}
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		return RuntimeProviderStatus{
			ID:       cfg.ProviderID,
			Protocol: cfg.ProviderProtocol,
			Model:    cfg.Model,
			BaseURL:  cfg.BaseURL,
		}
	}
	return RuntimeProviderStatus{
		ID:           profile.ID,
		Protocol:     string(profile.Protocol),
		Model:        profile.Model,
		BaseURL:      profile.BaseURL,
		Capabilities: profile.Capabilities,
	}
}
