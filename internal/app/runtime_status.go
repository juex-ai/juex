package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/prompt"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

// RuntimeStatusService assembles read-only runtime facts for presentation
// layers such as the web UI.
type RuntimeStatusService struct {
	cfg config.Config
}

func NewRuntimeStatusService(cfg config.Config) RuntimeStatusService {
	return RuntimeStatusService{cfg: cfg}
}

type RuntimeStatusOptions struct {
	MCPToolDescriptors map[string][]mcp.ToolDescriptor
	MCPErrors          map[string]string
	SkillCache         *RuntimeStatusSkillCache
	ScratchpadDir      string
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
	SystemPrompt RuntimeSystemPromptStatus
	Tools        RuntimeToolsStatus
	MCP          RuntimeMCPStatus
	Hooks        RuntimeHooksStatus
	Skills       RuntimeSkillsStatus
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

func (s RuntimeStatusService) Snapshot(opts RuntimeStatusOptions) (RuntimeStatus, error) {
	resourceGraph, err := ResolveRuntimeResourceGraph(s.cfg)
	if err != nil {
		return RuntimeStatus{}, err
	}
	skillStatus, skillLoader, err := s.skillsStatus(opts.SkillCache, resourceGraph.SkillDirs(), s.cfg.SkillPolicy())
	if err != nil {
		return RuntimeStatus{}, err
	}
	systemPrompt, err := s.systemPromptStatus(skillLoader, opts.ScratchpadDir)
	if err != nil {
		return RuntimeStatus{}, err
	}
	mcpStatus, err := s.mcpStatus(opts, resourceGraph.MCPConfigs())
	if err != nil {
		return RuntimeStatus{}, err
	}
	toolsStatus, err := s.toolsStatus()
	if err != nil {
		return RuntimeStatus{}, err
	}
	return RuntimeStatus{
		WorkDir:      s.absoluteWorkDir(),
		Provider:     providerRuntimeStatusFromConfig(s.cfg),
		Shell:        s.cfg.Shell,
		Sandbox:      s.cfg.SandboxPolicy(),
		SystemPrompt: systemPrompt,
		Tools:        toolsStatus,
		MCP:          mcpStatus,
		Hooks:        hooksStatus(resourceGraph.HooksConfig()),
		Skills:       skillStatus,
	}, nil
}

func (s RuntimeStatusService) toolsStatus() (RuntimeToolsStatus, error) {
	definitions := tools.DefaultBuiltinToolDefinitions(tools.BuiltinDefinitionOptions{
		Shell: toolsShellProfile(s.cfg.Shell),
	})
	definitions = append(definitions, skillToolDefinitions()...)
	definitions = append(definitions, memory.ToolDefinitions()...)
	definitions = append(definitions, juexruntime.GoalToolDefinitions()...)
	definitions = append(definitions, juexruntime.NotesToolDefinitions()...)
	definitions = append(definitions, observable.ToolDefinitions()...)
	return runtimeToolsStatusFromDefinitions(definitions, durationSeconds(s.cfg.RuntimeLimits().ToolTimeout))
}

func runtimeToolsStatusFromDefinitions(definitions []tools.ToolDefinition, defaultTimeoutSeconds int) (RuntimeToolsStatus, error) {
	groupOrder := []tools.ToolGroup{
		tools.ToolGroupFile,
		tools.ToolGroupChunkedWrite,
		tools.ToolGroupShell,
		tools.ToolGroupSearch,
		tools.ToolGroupSkill,
		tools.ToolGroupMemory,
		tools.ToolGroupSessionState,
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
			TimeoutSeconds: timeoutSeconds,
			MaxOutputBytes: maxOutputBytes,
		})
	}
	return RuntimeHooksStatus{Configured: len(commands), Commands: commands}
}

func (s RuntimeStatusService) systemPromptStatus(skillLoader *skills.Loader, scratchpadDir string) (RuntimeSystemPromptStatus, error) {
	var memStore *memory.Store
	if memoryDir := s.cfg.MemoryDir(); memoryDir != "" {
		memStore = memory.NewStore(memoryDir)
	}
	builder := &prompt.Builder{
		GlobalAgentsMDPath: s.cfg.GlobalAgentsMDPath(),
		AgentsMDDirs:       s.cfg.AgentsMDDirs(),
		Memory:             memStore,
		Skills:             skillLoader,
		ScratchpadDir:      scratchpadDir,
		WorkDir:            s.cfg.WorkDir,
		Shell:              prompt.ShellProfileFromConfig(s.cfg.Shell),
	}
	sections := builder.Sections()
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

func (s RuntimeStatusService) skillsStatus(cache *RuntimeStatusSkillCache, dirs []skills.Dir, policy config.SkillPolicy) (RuntimeSkillsStatus, *skills.Loader, error) {
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

func (s RuntimeStatusService) mcpStatus(opts RuntimeStatusOptions, refs []mcpConfigRef) (RuntimeMCPStatus, error) {
	servers, err := s.configuredMCPServers(refs)
	if err != nil {
		return RuntimeMCPStatus{}, err
	}
	connectedCount := 0
	errorCount := 0
	statuses := make([]RuntimeMCPServerStatus, 0, len(servers))
	defaultTimeoutSeconds := durationSeconds(s.cfg.RuntimeLimits().ToolTimeout)
	for _, server := range servers {
		descriptors, connected := opts.MCPToolDescriptors[server.Name]
		errText := opts.MCPErrors[server.Name]
		status := "not_started"
		projectedTools := runtimeMCPToolInfos(nil, defaultTimeoutSeconds)
		if errText != "" {
			status = "error"
			connected = false
		} else if connected {
			status = "connected"
			projectedTools = runtimeMCPToolInfos(descriptors, defaultTimeoutSeconds)
		}
		info := RuntimeMCPServerStatus{
			Name:      server.Name,
			Source:    server.Source,
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

func runtimeMCPToolInfos(descriptors []mcp.ToolDescriptor, defaultTimeoutSeconds int) []RuntimeToolInfo {
	infos := make([]RuntimeToolInfo, 0, len(descriptors))
	for _, descriptor := range descriptors {
		infos = append(infos, runtimeToolInfoFromDefinition(tools.ToolDefinition{
			Name:        descriptor.Name,
			Group:       tools.ToolGroupMCP,
			Description: descriptor.Description,
			Schema:      descriptor.InputSchema,
		}, defaultTimeoutSeconds))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func (s RuntimeStatusService) configuredMCPServers(refs []mcpConfigRef) ([]runtimeMCPServerConfig, error) {
	serversByName := map[string]runtimeMCPServerConfig{}
	_, merged, sources, err := loadMCPConfigRefs(refs, s.absoluteWorkDir())
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

func (s RuntimeStatusService) absoluteWorkDir() string {
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
