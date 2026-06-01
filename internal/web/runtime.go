package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/skills"
)

type runtimeStatusResponse struct {
	WorkDir      string             `json:"work_dir"`
	Provider     providerStatus     `json:"provider"`
	SystemPrompt systemPromptStatus `json:"system_prompt"`
	MCP          mcpStatus          `json:"mcp"`
	Skills       skillsStatus       `json:"skills"`
}

type providerStatus struct {
	ID           string                   `json:"id,omitempty"`
	Protocol     string                   `json:"protocol,omitempty"`
	Model        string                   `json:"model,omitempty"`
	BaseURL      string                   `json:"base_url,omitempty"`
	Capabilities llm.ProviderCapabilities `json:"capabilities"`
}

type systemPromptStatus struct {
	Count int                 `json:"count"`
	Items []systemPromptEntry `json:"items"`
}

type systemPromptEntry struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Tokens int    `json:"tokens"`
	Text   string `json:"text"`
}

type mcpStatus struct {
	Configured int             `json:"configured"`
	Connected  int             `json:"connected"`
	Errors     int             `json:"errors"`
	Servers    []mcpServerInfo `json:"servers"`
}

type mcpServerInfo struct {
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Status    string   `json:"status"`
	Connected bool     `json:"connected"`
	ToolCount int      `json:"tool_count"`
	Error     string   `json:"error,omitempty"`
}

type mcpServerConfig struct {
	Name   string
	Source string
	Spec   mcp.ServerSpec
}

type skillsStatus struct {
	Count int         `json:"count"`
	Items []skillInfo `json:"items"`
}

type skillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type,omitempty"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}

func (s *Server) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	status, err := s.runtimeStatus()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) runtimeStatus() (runtimeStatusResponse, error) {
	if err := s.ensureMCPStarted(context.Background()); err != nil {
		return runtimeStatusResponse{}, err
	}
	mcpServers, err := s.configuredMCPServerConfigs()
	if err != nil {
		return runtimeStatusResponse{}, err
	}
	connected := s.connectedMCPServers()
	managerTools := s.mcpToolCounts()
	for serverName, count := range managerTools {
		if connected[serverName] < count {
			connected[serverName] = count
		}
	}
	mcpErrors := s.mcpErrors()
	connectedCount := 0
	errorCount := 0
	servers := make([]mcpServerInfo, 0, len(mcpServers))
	for _, server := range mcpServers {
		toolCount := connected[server.Name]
		_, managerConnected := managerTools[server.Name]
		status := "not_started"
		errText := mcpErrors[server.Name]
		if toolCount > 0 || managerConnected {
			status = "connected"
		} else if errText != "" {
			status = "error"
		}
		info := mcpServerInfo{
			Name:      server.Name,
			Source:    server.Source,
			Command:   server.Spec.Command,
			Args:      append([]string(nil), server.Spec.Args...),
			Status:    status,
			Connected: toolCount > 0 || managerConnected,
			ToolCount: toolCount,
			Error:     errText,
		}
		if info.Connected {
			connectedCount++
		} else if info.Status == "error" {
			errorCount++
		}
		servers = append(servers, info)
	}

	skillStatus, err := s.cachedSkillsStatus()
	if err != nil {
		return runtimeStatusResponse{}, err
	}
	systemPrompt, err := s.systemPromptStatus()
	if err != nil {
		return runtimeStatusResponse{}, err
	}

	return runtimeStatusResponse{
		WorkDir:      s.absoluteWorkDir(),
		Provider:     s.providerStatus(),
		SystemPrompt: systemPrompt,
		MCP: mcpStatus{
			Configured: len(servers),
			Connected:  connectedCount,
			Errors:     errorCount,
			Servers:    servers,
		},
		Skills: skillStatus,
	}, nil
}

func (s *Server) systemPromptStatus() (systemPromptStatus, error) {
	skillLoader := skills.NewLoader(s.opts.Cfg.SkillDirs()...)
	if err := skillLoader.Load(); err != nil {
		return systemPromptStatus{}, err
	}
	var globalAgents string
	if s.opts.Cfg.HomeAgentsDir != "" {
		globalAgents = filepath.Join(s.opts.Cfg.HomeAgentsDir, "AGENTS.md")
	}
	var memStore *memory.Store
	if memoryDir := s.opts.Cfg.MemoryDir(); memoryDir != "" {
		memStore = memory.NewStore(memoryDir)
	}
	builder := &prompt.Builder{
		GlobalAgentsMDPath: globalAgents,
		AgentsMDDirs:       s.opts.Cfg.AgentsMDDirs(),
		Memory:             memStore,
		Skills:             skillLoader,
	}
	sections := builder.Sections()
	items := make([]systemPromptEntry, 0, len(sections))
	for _, section := range sections {
		items = append(items, systemPromptEntry{
			Key:    section.Key,
			Label:  runtimePromptLabel(section),
			Source: runtimePromptSource(section),
			Path:   section.Path,
			Tokens: estimateRuntimePromptTokens(section.Text),
			Text:   section.Text,
		})
	}
	return systemPromptStatus{
		Count: len(items),
		Items: items,
	}, nil
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

func estimateRuntimePromptTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func (s *Server) absoluteWorkDir() string {
	workDir := s.opts.Cfg.WorkDir
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

func (s *Server) providerStatus() providerStatus {
	if s.opts.Cfg.ProviderID == "" && s.opts.Cfg.ProviderProtocol == "" {
		return providerStatus{
			Model:   s.opts.Cfg.Model,
			BaseURL: s.opts.Cfg.BaseURL,
		}
	}
	profile, err := s.opts.Cfg.ProviderProfile()
	if err != nil {
		return providerStatus{
			ID:       s.opts.Cfg.ProviderID,
			Protocol: s.opts.Cfg.ProviderProtocol,
			Model:    s.opts.Cfg.Model,
			BaseURL:  s.opts.Cfg.BaseURL,
		}
	}
	return providerStatus{
		ID:           profile.ID,
		Protocol:     string(profile.Protocol),
		Model:        profile.Model,
		BaseURL:      profile.BaseURL,
		Capabilities: profile.Capabilities,
	}
}

func (s *Server) cachedSkillsStatus() (skillsStatus, error) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeSkills != nil {
		return cloneSkillsStatus(*s.runtimeSkills), nil
	}

	skillLoader := skills.NewLoader(s.opts.Cfg.SkillDirs()...)
	if err := skillLoader.Load(); err != nil {
		return skillsStatus{}, err
	}
	loadedSkills := skillLoader.All()
	skillItems := make([]skillInfo, 0, len(loadedSkills))
	for _, skill := range loadedSkills {
		skillItems = append(skillItems, skillInfo{
			Name:        skill.Name,
			Description: skill.Description,
			Type:        skill.Type,
			Source:      skill.Source,
			Path:        skill.Path,
		})
	}
	sort.Slice(skillItems, func(i, j int) bool {
		return runtimeSourceLess(skillItems[i].Source, skillItems[i].Name, skillItems[j].Source, skillItems[j].Name)
	})
	status := skillsStatus{
		Count: len(skillItems),
		Items: skillItems,
	}
	s.runtimeSkills = &status
	return cloneSkillsStatus(status), nil
}

func cloneSkillsStatus(status skillsStatus) skillsStatus {
	status.Items = append([]skillInfo(nil), status.Items...)
	return status
}

func (s *Server) configuredMCPServerConfigs() ([]mcpServerConfig, error) {
	serversByName := map[string]mcpServerConfig{}
	for _, path := range s.opts.Cfg.MCPConfigPaths() {
		cfg, err := mcp.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		cfg = mcp.PrepareConfig(cfg, s.absoluteWorkDir())
		source := s.runtimeSourceForPath(path)
		for name, spec := range cfg.MCPServers {
			serversByName[name] = mcpServerConfig{
				Name:   name,
				Source: source,
				Spec:   spec,
			}
		}
	}
	servers := make([]mcpServerConfig, 0, len(serversByName))
	for _, server := range serversByName {
		servers = append(servers, server)
	}
	sort.Slice(servers, func(i, j int) bool {
		return runtimeSourceLess(servers[i].Source, servers[i].Name, servers[j].Source, servers[j].Name)
	})
	return servers, nil
}

func (s *Server) loadMCPConfigs() ([]mcp.Config, error) {
	var configs []mcp.Config
	for _, path := range s.opts.Cfg.MCPConfigPaths() {
		cfg, err := mcp.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		cfg = mcp.PrepareConfig(cfg, s.absoluteWorkDir())
		if len(cfg.MCPServers) > 0 {
			configs = append(configs, cfg)
		}
	}
	return configs, nil
}

func (s *Server) runtimeSourceForPath(path string) string {
	cleanPath := filepath.Clean(path)
	if s.opts.Cfg.WorkDir != "" {
		projectPath := filepath.Join(s.opts.Cfg.WorkDir, ".agents", "mcp.json")
		if cleanPath == filepath.Clean(projectPath) {
			return "project"
		}
	}
	if s.opts.Cfg.HomeAgentsDir != "" {
		userPath := filepath.Join(s.opts.Cfg.HomeAgentsDir, "mcp.json")
		if cleanPath == filepath.Clean(userPath) {
			return "user"
		}
	}
	return "runtime"
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
		return 1
	default:
		return 2
	}
}

func (s *Server) connectedMCPServers() map[string]int {
	toolsByServer := map[string]map[string]struct{}{}
	s.sessions.Range(func(_, v any) bool {
		as := v.(*activeSession)
		if as.app.Engine == nil || as.app.Engine.Tools == nil {
			return true
		}
		for _, tool := range as.app.Engine.Tools.List() {
			serverName, ok := mcpServerFromToolName(tool.Name)
			if ok {
				if toolsByServer[serverName] == nil {
					toolsByServer[serverName] = map[string]struct{}{}
				}
				toolsByServer[serverName][tool.Name] = struct{}{}
			}
		}
		return true
	})
	connected := map[string]int{}
	for serverName, tools := range toolsByServer {
		connected[serverName] = len(tools)
	}
	return connected
}

func mcpServerFromToolName(name string) (string, bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	server, _, ok := strings.Cut(rest, "__")
	if !ok || server == "" {
		return "", false
	}
	return server, true
}
