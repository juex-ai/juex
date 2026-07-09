package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/sandbox"
)

type runtimeStatusResponse struct {
	WorkDir      string              `json:"work_dir"`
	Provider     providerStatus      `json:"provider"`
	Shell        config.ShellProfile `json:"shell"`
	Sandbox      sandbox.Policy      `json:"sandbox"`
	SystemPrompt systemPromptStatus  `json:"system_prompt"`
	MCP          mcpStatus           `json:"mcp"`
	Hooks        hooksStatus         `json:"hooks"`
	Skills       skillsStatus        `json:"skills"`
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

type hooksStatus struct {
	Configured int        `json:"configured"`
	Commands   []hookInfo `json:"commands"`
}

type hookInfo struct {
	Name           string   `json:"name"`
	Source         string   `json:"source,omitempty"`
	Events         []string `json:"events"`
	Tools          []string `json:"tools,omitempty"`
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxOutputBytes int      `json:"max_output_bytes"`
}

type skillsStatus struct {
	Count    int                 `json:"count"`
	Items    []skillInfo         `json:"items"`
	Filtered []skillFilteredInfo `json:"filtered,omitempty"`
	Prompt   skillPromptStatus   `json:"prompt"`
}

type skillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type,omitempty"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}

type skillFilteredInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type skillPromptStatus struct {
	BudgetChars int                `json:"budget_chars"`
	UsedChars   int                `json:"used_chars"`
	Compacted   bool               `json:"compacted"`
	Omitted     []skillOmittedInfo `json:"omitted,omitempty"`
}

type skillOmittedInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Reason string `json:"reason"`
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
	toolCounts := s.connectedMCPServers()
	for serverName, count := range s.mcpToolCounts() {
		if existing, ok := toolCounts[serverName]; !ok || existing < count {
			toolCounts[serverName] = count
		}
	}
	status, err := app.NewRuntimeStatusService(s.opts.Cfg).Snapshot(app.RuntimeStatusOptions{
		MCPToolCounts: toolCounts,
		MCPErrors:     s.mcpErrors(),
		SkillCache:    s.runtimeSkills,
	})
	if err != nil {
		return runtimeStatusResponse{}, err
	}
	return runtimeStatusResponseFromApp(status), nil
}

func runtimeStatusResponseFromApp(status app.RuntimeStatus) runtimeStatusResponse {
	return runtimeStatusResponse{
		WorkDir:      status.WorkDir,
		Provider:     providerStatusFromApp(status.Provider),
		Shell:        status.Shell,
		Sandbox:      status.Sandbox,
		SystemPrompt: systemPromptStatusFromApp(status.SystemPrompt),
		MCP:          mcpStatusFromApp(status.MCP),
		Hooks:        hooksStatusFromApp(status.Hooks),
		Skills:       skillsStatusFromApp(status.Skills),
	}
}

func providerStatusFromApp(status app.RuntimeProviderStatus) providerStatus {
	return providerStatus{
		ID:           status.ID,
		Protocol:     status.Protocol,
		Model:        status.Model,
		BaseURL:      status.BaseURL,
		Capabilities: status.Capabilities,
	}
}

func systemPromptStatusFromApp(status app.RuntimeSystemPromptStatus) systemPromptStatus {
	items := make([]systemPromptEntry, 0, len(status.Items))
	for _, item := range status.Items {
		items = append(items, systemPromptEntry{
			Key:    item.Key,
			Label:  item.Label,
			Source: item.Source,
			Path:   item.Path,
			Tokens: item.Tokens,
			Text:   item.Text,
		})
	}
	return systemPromptStatus{Count: status.Count, Items: items}
}

func mcpStatusFromApp(status app.RuntimeMCPStatus) mcpStatus {
	servers := make([]mcpServerInfo, 0, len(status.Servers))
	for _, server := range status.Servers {
		servers = append(servers, mcpServerInfo{
			Name:      server.Name,
			Source:    server.Source,
			Command:   server.Command,
			Args:      append([]string(nil), server.Args...),
			Status:    server.Status,
			Connected: server.Connected,
			ToolCount: server.ToolCount,
			Error:     server.Error,
		})
	}
	return mcpStatus{
		Configured: status.Configured,
		Connected:  status.Connected,
		Errors:     status.Errors,
		Servers:    servers,
	}
}

func hooksStatusFromApp(status app.RuntimeHooksStatus) hooksStatus {
	commands := make([]hookInfo, 0, len(status.Commands))
	for _, command := range status.Commands {
		commands = append(commands, hookInfo{
			Name:           command.Name,
			Source:         command.Source,
			Events:         append([]string(nil), command.Events...),
			Tools:          append([]string(nil), command.Tools...),
			Command:        append([]string(nil), command.Command...),
			TimeoutSeconds: command.TimeoutSeconds,
			MaxOutputBytes: command.MaxOutputBytes,
		})
	}
	return hooksStatus{Configured: status.Configured, Commands: commands}
}

func skillsStatusFromApp(status app.RuntimeSkillsStatus) skillsStatus {
	items := make([]skillInfo, 0, len(status.Items))
	for _, item := range status.Items {
		items = append(items, skillInfo{
			Name:        item.Name,
			Description: item.Description,
			Type:        item.Type,
			Source:      item.Source,
			Path:        item.Path,
		})
	}
	filtered := make([]skillFilteredInfo, 0, len(status.Filtered))
	for _, item := range status.Filtered {
		filtered = append(filtered, skillFilteredInfo{Name: item.Name, Source: item.Source, Reason: item.Reason})
	}
	omitted := make([]skillOmittedInfo, 0, len(status.Prompt.Omitted))
	for _, item := range status.Prompt.Omitted {
		omitted = append(omitted, skillOmittedInfo{Name: item.Name, Source: item.Source, Reason: item.Reason})
	}
	return skillsStatus{
		Count:    status.Count,
		Items:    items,
		Filtered: filtered,
		Prompt: skillPromptStatus{
			BudgetChars: status.Prompt.BudgetChars,
			UsedChars:   status.Prompt.UsedChars,
			Compacted:   status.Prompt.Compacted,
			Omitted:     omitted,
		},
	}
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

func (s *Server) loadMCPConfigs() ([]mcp.Config, error) {
	return app.LoadMCPConfigs(s.opts.Cfg, s.absoluteWorkDir())
}

func (s *Server) connectedMCPServers() map[string]int {
	toolsByServer := map[string]map[string]struct{}{}
	s.sessions.Range(func(_, v any) bool {
		as := v.(*activeSession)
		if as.app.Engine == nil || as.app.Engine.Tools == nil {
			return true
		}
		for _, tool := range as.app.Engine.Tools.List() {
			serverName, _, ok := mcp.ParseToolName(tool.Name)
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
