package web

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/skills"
)

type runtimeStatusResponse struct {
	MCP    mcpStatus    `json:"mcp"`
	Skills skillsStatus `json:"skills"`
}

type mcpStatus struct {
	Configured int             `json:"configured"`
	Connected  int             `json:"connected"`
	Errors     int             `json:"errors"`
	Servers    []mcpServerInfo `json:"servers"`
}

type mcpServerInfo struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Status    string   `json:"status"`
	Connected bool     `json:"connected"`
	ToolCount int      `json:"tool_count"`
	Error     string   `json:"error,omitempty"`
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
	mcpServers, err := s.configuredMCPServers()
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
	for name, spec := range mcpServers {
		toolCount := connected[name]
		_, managerConnected := managerTools[name]
		status := "not_started"
		errText := mcpErrors[name]
		if toolCount > 0 || managerConnected {
			status = "connected"
		} else if errText != "" {
			status = "error"
		}
		info := mcpServerInfo{
			Name:      name,
			Command:   spec.Command,
			Args:      append([]string(nil), spec.Args...),
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
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })

	skillStatus, err := s.cachedSkillsStatus()
	if err != nil {
		return runtimeStatusResponse{}, err
	}

	return runtimeStatusResponse{
		MCP: mcpStatus{
			Configured: len(servers),
			Connected:  connectedCount,
			Errors:     errorCount,
			Servers:    servers,
		},
		Skills: skillStatus,
	}, nil
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

func (s *Server) configuredMCPServers() (map[string]mcp.ServerSpec, error) {
	configs, err := s.loadMCPConfigs()
	if err != nil {
		return nil, err
	}
	return mcp.MergeConfigs(configs).MCPServers, nil
}

func (s *Server) loadMCPConfigs() ([]mcp.Config, error) {
	var configs []mcp.Config
	for _, path := range s.opts.Cfg.MCPConfigPaths() {
		cfg, err := mcp.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		if len(cfg.MCPServers) > 0 {
			configs = append(configs, cfg)
		}
	}
	return configs, nil
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
