package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
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
	ActiveModules      *RuntimeModuleSnapshot
	MCPToolDescriptors map[string][]mcp.ToolDescriptor
	MCPErrors          map[string]string
	MCPConnectionSpecs map[string]mcp.RuntimeConnectionSpec
	AgentRuntime       *AgentRuntimeResolution
}

// RuntimeModuleSnapshot is a leased view of the active, sealed Module sets.
// Callers obtain it through App.ReadRuntimeModuleSnapshot so Session
// replacement and shutdown cannot invalidate the sets during projection.
type RuntimeModuleSnapshot struct {
	Runtime        *runtimemodule.Set
	Session        *runtimemodule.Set
	RuntimeContext runtimemodule.RuntimeContext
	SessionContext runtimemodule.SessionContext
	Skills         []skills.Skill
	FilteredSkills []skills.FilteredSkill
	SkillPrompt    skills.PromptBudgetReport
}

type RuntimeStatus struct {
	WorkDir      string
	Modules      []RuntimeModuleStatus
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

type RuntimeModuleStatus struct {
	ID    string
	Scope string
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

// ReadRuntimeModuleSnapshot holds the App and Session publication leases while
// fn projects the currently active Runtime and Session Module sets.
func (a *App) ReadRuntimeModuleSnapshot(fn func(RuntimeModuleSnapshot) error) error {
	if a == nil || fn == nil {
		return fmt.Errorf("runtime status: active App and snapshot reader are required")
	}
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.Engine == nil || a.runtimeModules == nil {
		return fmt.Errorf("runtime status: active Runtime Module set is unavailable")
	}
	sessionRuntime := a.Engine.SessionRuntimeSnapshot()
	if sessionRuntime.Modules == nil || sessionRuntime.Session == nil {
		return fmt.Errorf("runtime status: active Session Module set is unavailable")
	}
	return fn(RuntimeModuleSnapshot{
		Runtime:        a.runtimeModules,
		Session:        sessionRuntime.Modules,
		RuntimeContext: a.runtimeModuleContext,
		SessionContext: sessionModuleContext(sessionRuntime.Session),
		Skills:         append([]skills.Skill(nil), a.skills...),
		FilteredSkills: append([]skills.FilteredSkill(nil), a.skillFilteredItems...),
		SkillPrompt:    cloneSkillPromptReport(a.skillPrompt),
	})
}

func cloneSkillPromptReport(report skills.PromptBudgetReport) skills.PromptBudgetReport {
	report.Omitted = append([]skills.PromptOmittedSkill(nil), report.Omitted...)
	return report
}

func (s RuntimeCatalogService) Snapshot(opts RuntimeStatusOptions) (RuntimeStatus, error) {
	active := opts.ActiveModules
	if active == nil || active.Runtime == nil || active.Session == nil {
		return RuntimeStatus{}, fmt.Errorf("runtime status: active sealed Runtime and Session Module sets are required")
	}
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
	skillStatus := runtimeSkillsStatusFromSnapshot(*active)
	systemPrompt, err := systemPromptStatusFromActiveModules(*active)
	if err != nil {
		return RuntimeStatus{}, err
	}
	entries := activeToolEntries(*active)
	mcpEnabled := activeModuleEnabled(*active, mcp.ModuleID)
	mcpStatus, err := s.mcpStatus(opts, resourceGraph.MCPConfigs(), agentRuntime.Environment(), entries, mcpEnabled)
	if err != nil {
		return RuntimeStatus{}, err
	}
	toolsStatus, err := runtimeToolsStatusFromActiveCatalogs(durationSeconds(s.cfg.RuntimeLimits().ToolTimeout), entries)
	if err != nil {
		return RuntimeStatus{}, err
	}
	hookStatus := RuntimeHooksStatus{Commands: []RuntimeHookInfo{}}
	if activeModuleEnabled(*active, hooks.ModuleID) {
		hookStatus = hooksStatus(resourceGraph.HooksConfig())
	}
	extensionsStatus, err := runtimeExtensionsStatus(
		resourceGraph,
		skillStatus,
		mcpStatus,
		hookStatus,
		agentRuntime.EnvironmentDeclarations(),
		activeModuleEnabled(*active, observable.ModuleID),
	)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return RuntimeStatus{
		WorkDir:      s.absoluteWorkDir(),
		Modules:      runtimeModuleStatuses(*active),
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

func runtimeExtensionsStatus(graph RuntimeResourceGraph, skills RuntimeSkillsStatus, mcpStatus RuntimeMCPStatus, hookStatus RuntimeHooksStatus, environmentDeclarations []RuntimeExtensionEnvironmentDeclaration, observablesEnabled bool) (RuntimeExtensionsStatus, error) {
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
	if observablesEnabled {
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
	}
	return RuntimeExtensionsStatus{Count: len(items), Items: items}, nil
}

func activeToolEntries(active RuntimeModuleSnapshot) []runtimemodule.ToolEntry {
	entries := active.Runtime.ToolCatalog().Entries()
	entries = append(entries, active.Session.ToolCatalog().Entries()...)
	return entries
}

func runtimeToolsStatusFromActiveCatalogs(defaultTimeoutSeconds int, entries []runtimemodule.ToolEntry) (RuntimeToolsStatus, error) {
	filtered := make([]runtimemodule.ToolEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Tool.Group == tools.ToolGroupMCP {
			continue
		}
		filtered = append(filtered, entry)
	}
	return runtimeToolsStatusFromEntries(defaultTimeoutSeconds, filtered)
}

func runtimeToolsStatusFromEntries(defaultTimeoutSeconds int, entries []runtimemodule.ToolEntry) (RuntimeToolsStatus, error) {
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

func systemPromptStatusFromActiveModules(active RuntimeModuleSnapshot) (RuntimeSystemPromptStatus, error) {
	sections, err := runtimemodule.CollectContext(context.Background(), runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Runtime: active.RuntimeContext,
		Session: &active.SessionContext,
	}, active.Runtime, active.Session)
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

func runtimePromptLabel(section runtimemodule.ContextSection) string {
	if section.Label != "" {
		return section.Label
	}
	return section.Key
}

func runtimePromptSource(section runtimemodule.ContextSection) string {
	if section.Source != "" {
		return section.Source
	}
	return "runtime"
}

func runtimeSkillsStatusFromSnapshot(active RuntimeModuleSnapshot) RuntimeSkillsStatus {
	items := make([]RuntimeSkillInfo, 0, len(active.Skills))
	for _, skill := range active.Skills {
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
	filtered := make([]RuntimeSkillFilteredInfo, 0, len(active.FilteredSkills))
	for _, item := range active.FilteredSkills {
		filtered = append(filtered, RuntimeSkillFilteredInfo{Name: item.Name, Source: item.Source, Reason: item.Reason})
	}
	report := active.SkillPrompt
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
	}
}

func runtimeModuleStatuses(active RuntimeModuleSnapshot) []RuntimeModuleStatus {
	descriptors := active.Runtime.Descriptors()
	descriptors = append(descriptors, active.Session.Descriptors()...)
	items := make([]RuntimeModuleStatus, 0, len(descriptors))
	for _, descriptor := range descriptors {
		items = append(items, RuntimeModuleStatus{ID: string(descriptor.ID), Scope: string(descriptor.Scope)})
	}
	return items
}

func activeModuleEnabled(active RuntimeModuleSnapshot, id runtimemodule.ID) bool {
	for _, descriptor := range active.Runtime.Descriptors() {
		if descriptor.ID == id {
			return true
		}
	}
	for _, descriptor := range active.Session.Descriptors() {
		if descriptor.ID == id {
			return true
		}
	}
	return false
}

type runtimeMCPServerConfig struct {
	Name   string
	Source string
	Spec   mcp.ServerSpec
}

func (s RuntimeCatalogService) mcpStatus(opts RuntimeStatusOptions, refs []mcpConfigRef, runtimeEnvironment environment.Snapshot, entries []runtimemodule.ToolEntry, enabled bool) (RuntimeMCPStatus, error) {
	if !enabled {
		return RuntimeMCPStatus{Servers: []RuntimeMCPServerStatus{}}, nil
	}
	mcpCatalog := make(map[string]runtimemodule.ToolEntry)
	for _, entry := range entries {
		if entry.ModuleID == mcp.ModuleID {
			mcpCatalog[entry.Tool.Name] = entry
		}
	}
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
			projectedTools, err = runtimeMCPToolInfos(server.Name, descriptors, defaultTimeoutSeconds, mcpCatalog)
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

func runtimeMCPToolInfos(serverName string, descriptors []mcp.ToolDescriptor, defaultTimeoutSeconds int, catalog map[string]runtimemodule.ToolEntry) ([]RuntimeToolInfo, error) {
	infos := make([]RuntimeToolInfo, 0, len(descriptors))
	for _, descriptor := range descriptors {
		entry, ok := catalog[mcp.ToolName(serverName, descriptor.Name)]
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
