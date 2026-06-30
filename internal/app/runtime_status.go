package app

import (
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/prompt"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/skills"
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
	MCPToolCounts map[string]int
	MCPErrors     map[string]string
	SkillCache    *RuntimeStatusSkillCache
}

// RuntimeStatusSkillCache caches loaded skills for repeated status snapshots.
// The cache is keyed by the resolved skill directory list, so tests and callers
// that swap config directories get a fresh load without leaking skills.Loader
// details into presentation layers.
type RuntimeStatusSkillCache struct {
	mu     sync.Mutex
	dirs   []skills.Dir
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
	SystemPrompt RuntimeSystemPromptStatus
	MCP          RuntimeMCPStatus
	Hooks        RuntimeHooksStatus
	Skills       RuntimeSkillsStatus
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
	Count int
	Items []RuntimeSkillInfo
}

type RuntimeSkillInfo struct {
	Name        string
	Description string
	Type        string
	Source      string
	Path        string
}

func (s RuntimeStatusService) Snapshot(opts RuntimeStatusOptions) (RuntimeStatus, error) {
	resourceGraph, err := ResolveRuntimeResourceGraph(s.cfg)
	if err != nil {
		return RuntimeStatus{}, err
	}
	skillStatus, skillLoader, err := s.skillsStatus(opts.SkillCache, resourceGraph.SkillDirs())
	if err != nil {
		return RuntimeStatus{}, err
	}
	systemPrompt, err := s.systemPromptStatus(skillLoader)
	if err != nil {
		return RuntimeStatus{}, err
	}
	mcpStatus, err := s.mcpStatus(opts, resourceGraph.MCPConfigs())
	if err != nil {
		return RuntimeStatus{}, err
	}
	return RuntimeStatus{
		WorkDir:      s.absoluteWorkDir(),
		Provider:     providerRuntimeStatusFromConfig(s.cfg),
		Shell:        s.cfg.Shell,
		SystemPrompt: systemPrompt,
		MCP:          mcpStatus,
		Hooks:        hooksStatus(resourceGraph.HooksConfig()),
		Skills:       skillStatus,
	}, nil
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

func (s RuntimeStatusService) systemPromptStatus(skillLoader *skills.Loader) (RuntimeSystemPromptStatus, error) {
	var memStore *memory.Store
	if memoryDir := s.cfg.MemoryDir(); memoryDir != "" {
		memStore = memory.NewStore(memoryDir)
	}
	builder := &prompt.Builder{
		GlobalAgentsMDPath: s.cfg.GlobalAgentsMDPath(),
		AgentsMDDirs:       s.cfg.AgentsMDDirs(),
		Memory:             memStore,
		Skills:             skillLoader,
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

func (s RuntimeStatusService) skillsStatus(cache *RuntimeStatusSkillCache, dirs []skills.Dir) (RuntimeSkillsStatus, *skills.Loader, error) {
	if cache != nil {
		return cache.snapshot(dirs)
	}
	return loadRuntimeSkills(dirs)
}

func (c *RuntimeStatusSkillCache) snapshot(dirs []skills.Dir) (RuntimeSkillsStatus, *skills.Loader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && skillDirsEqual(c.dirs, dirs) {
		return cloneRuntimeSkillsStatus(c.status), c.loader, nil
	}
	status, loader, err := loadRuntimeSkills(dirs)
	if err != nil {
		return RuntimeSkillsStatus{}, nil, err
	}
	c.dirs = append([]skills.Dir(nil), dirs...)
	c.status = cloneRuntimeSkillsStatus(status)
	c.loader = loader
	c.loaded = true
	return status, loader, nil
}

func loadRuntimeSkills(dirs []skills.Dir) (RuntimeSkillsStatus, *skills.Loader, error) {
	skillLoader := skills.NewLoaderFromDirs(dirs)
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
	return RuntimeSkillsStatus{Count: len(items), Items: items}, skillLoader, nil
}

func cloneRuntimeSkillsStatus(status RuntimeSkillsStatus) RuntimeSkillsStatus {
	status.Items = append([]RuntimeSkillInfo(nil), status.Items...)
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
	for _, server := range servers {
		toolCount, connected := opts.MCPToolCounts[server.Name]
		errText := opts.MCPErrors[server.Name]
		status := "not_started"
		if connected {
			status = "connected"
		} else if errText != "" {
			status = "error"
		}
		info := RuntimeMCPServerStatus{
			Name:      server.Name,
			Source:    server.Source,
			Command:   server.Spec.Command,
			Args:      append([]string(nil), server.Spec.Args...),
			Status:    status,
			Connected: connected,
			ToolCount: toolCount,
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
