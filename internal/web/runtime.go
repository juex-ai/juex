package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/sandbox"
)

type runtimeStatusResponse struct {
	StartTime    string              `json:"start_time"`
	WorkDir      string              `json:"work_dir"`
	Modules      []runtimeModuleInfo `json:"modules"`
	Provider     providerStatus      `json:"provider"`
	Shell        config.ShellProfile `json:"shell"`
	Sandbox      sandbox.Policy      `json:"sandbox"`
	Extensions   extensionsStatus    `json:"extensions"`
	SystemPrompt systemPromptStatus  `json:"system_prompt"`
	Tools        runtimeToolsStatus  `json:"tools"`
	MCP          mcpStatus           `json:"mcp"`
	Hooks        hooksStatus         `json:"hooks"`
	Skills       skillsStatus        `json:"skills"`
}

type runtimeModuleInfo struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
}

type extensionsStatus struct {
	Count int             `json:"count"`
	Items []extensionInfo `json:"items"`
}

type extensionInfo struct {
	ManifestVersion int                        `json:"manifest_version"`
	Name            string                     `json:"name"`
	Version         string                     `json:"version"`
	Description     string                     `json:"description,omitempty"`
	DisplayName     string                     `json:"display_name,omitempty"`
	Author          string                     `json:"author,omitempty"`
	Homepage        string                     `json:"homepage,omitempty"`
	Repository      string                     `json:"repository,omitempty"`
	License         string                     `json:"license,omitempty"`
	Requirements    []extensionRequirementInfo `json:"requirements,omitempty"`
	Scope           string                     `json:"scope"`
	Path            string                     `json:"path"`
	Resources       extensionResourceCounts    `json:"resources"`
	Environment     []extensionEnvironmentInfo `json:"environment"`
}

type extensionRequirementInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

type extensionEnvironmentInfo struct {
	Name             string `json:"name"`
	Source           string `json:"source"`
	Status           string `json:"status"`
	ShadowedBySource string `json:"shadowed_by_source,omitempty"`
	ShadowedByPath   string `json:"shadowed_by_path,omitempty"`
}

type extensionResourceCounts struct {
	Skills      int `json:"skills"`
	MCPServers  int `json:"mcp_servers"`
	Hooks       int `json:"hooks"`
	Observables int `json:"observables"`
}

type runtimeToolsStatus struct {
	Count  int                    `json:"count"`
	Groups []runtimeToolGroupInfo `json:"groups"`
}

type runtimeToolGroupInfo struct {
	Group string            `json:"group"`
	Tools []runtimeToolInfo `json:"tools"`
}

type runtimeToolInfo struct {
	Name        string             `json:"name"`
	Module      string             `json:"module,omitempty"`
	Description string             `json:"description"`
	Schema      map[string]any     `json:"schema"`
	Timeout     runtimeToolTimeout `json:"timeout"`
}

type runtimeToolTimeout struct {
	Mode    string `json:"mode"`
	Seconds int    `json:"seconds"`
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
	Name      string            `json:"name"`
	Source    string            `json:"source"`
	Type      string            `json:"type"`
	URL       string            `json:"url,omitempty"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Status    string            `json:"status"`
	Connected bool              `json:"connected"`
	ToolCount int               `json:"tool_count"`
	Tools     []runtimeToolInfo `json:"tools"`
	Error     string            `json:"error,omitempty"`
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
	Required       bool     `json:"required"`
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
	runtimeEnvironment := s.opts.Cfg.EnvironmentSnapshot()
	if agentRuntime, resolveErr := s.resolveAgentRuntime(); resolveErr == nil {
		runtimeEnvironment = agentRuntime.Environment()
	}
	if err := writeRuntimeStatusJSON(w, http.StatusOK, status, runtimeEnvironment); err != nil {
		s.logVerbose("juex listen: write runtime status: %v", err)
	}
}

func writeRuntimeStatusJSON(w http.ResponseWriter, httpStatus int, status runtimeStatusResponse, snapshot environment.Snapshot) error {
	publicWorkDir := status.WorkDir
	publicExtensions := status.Extensions
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	data, _, err = snapshot.RedactConfiguredJSON(data)
	if err != nil {
		return err
	}
	var redacted runtimeStatusResponse
	if err := json.Unmarshal(data, &redacted); err != nil {
		return err
	}
	// These fields are already public runtime metadata. A default such as
	// ${WORKDIR} or ${JUEX_EXT_DIR} may equal them byte-for-byte, but that does
	// not turn the public path into an environment-value disclosure.
	redacted.WorkDir = publicWorkDir
	restorePublicExtensionStructure(&redacted.Extensions, publicExtensions)
	writeJSON(w, httpStatus, redacted)
	return nil
}

func restorePublicExtensionStructure(redacted *extensionsStatus, public extensionsStatus) {
	redacted.Count = public.Count
	for i := range redacted.Items {
		if i >= len(public.Items) {
			break
		}
		redacted.Items[i].ManifestVersion = public.Items[i].ManifestVersion
		redacted.Items[i].Name = public.Items[i].Name
		redacted.Items[i].Scope = public.Items[i].Scope
		redacted.Items[i].Path = public.Items[i].Path
		redacted.Items[i].Resources = public.Items[i].Resources
		redacted.Items[i].Environment = public.Items[i].Environment
	}
}

func (s *Server) runtimeStatus() (runtimeStatusResponse, error) {
	if err := s.ensureMCPStarted(context.Background()); err != nil {
		return runtimeStatusResponse{}, err
	}
	active, err := s.getCurrentActiveSession(context.Background())
	if errors.Is(err, os.ErrNotExist) {
		active, err = s.openSession(context.Background(), "", app.SessionModeNewPrimary)
	}
	if err != nil {
		return runtimeStatusResponse{}, err
	}
	agentRuntime, err := s.resolveAgentRuntime()
	if err != nil {
		return runtimeStatusResponse{}, err
	}
	var status app.RuntimeStatus
	err = active.app.ReadRuntimeModuleSnapshot(func(snapshot app.RuntimeModuleSnapshot) error {
		var snapshotErr error
		status, snapshotErr = app.NewRuntimeCatalogService(s.opts.Cfg).Snapshot(app.RuntimeStatusOptions{
			ActiveModules:      &snapshot,
			MCPToolDescriptors: s.mcpToolDescriptors(),
			MCPErrors:          s.mcpErrors(),
			MCPConnectionSpecs: s.mcpConnectionSpecs(),
			AgentRuntime:       &agentRuntime,
		})
		return snapshotErr
	})
	if err != nil {
		return runtimeStatusResponse{}, err
	}
	response := runtimeStatusResponseFromApp(status)
	response.StartTime = s.startedAt.Format(time.RFC3339Nano)
	return response, nil
}

func runtimeStatusResponseFromApp(status app.RuntimeStatus) runtimeStatusResponse {
	return runtimeStatusResponse{
		WorkDir:      status.WorkDir,
		Modules:      runtimeModulesFromApp(status.Modules),
		Provider:     providerStatusFromApp(status.Provider),
		Shell:        status.Shell,
		Sandbox:      status.Sandbox,
		Extensions:   extensionsStatusFromApp(status.Extensions),
		SystemPrompt: systemPromptStatusFromApp(status.SystemPrompt),
		Tools:        runtimeToolsStatusFromApp(status.Tools),
		MCP:          mcpStatusFromApp(status.MCP),
		Hooks:        hooksStatusFromApp(status.Hooks),
		Skills:       skillsStatusFromApp(status.Skills),
	}
}

func runtimeModulesFromApp(modules []app.RuntimeModuleStatus) []runtimeModuleInfo {
	items := make([]runtimeModuleInfo, 0, len(modules))
	for _, module := range modules {
		items = append(items, runtimeModuleInfo{ID: module.ID, Scope: module.Scope})
	}
	return items
}

func extensionsStatusFromApp(status app.RuntimeExtensionsStatus) extensionsStatus {
	items := make([]extensionInfo, 0, len(status.Items))
	for _, item := range status.Items {
		requirements := make([]extensionRequirementInfo, 0, len(item.Requirements))
		for _, requirement := range item.Requirements {
			requirements = append(requirements, extensionRequirementInfo{
				Name: requirement.Name, Description: requirement.Description, URL: requirement.URL,
			})
		}
		environmentItems := make([]extensionEnvironmentInfo, 0, len(item.Environment))
		for _, declaration := range item.Environment {
			environmentItems = append(environmentItems, extensionEnvironmentInfo{
				Name: declaration.Name, Source: declaration.Source, Status: string(declaration.Status),
				ShadowedBySource: declaration.ShadowedBySource, ShadowedByPath: declaration.ShadowedByPath,
			})
		}
		items = append(items, extensionInfo{
			ManifestVersion: item.ManifestVersion,
			Name:            item.Name,
			Version:         item.Version,
			Description:     item.Description,
			DisplayName:     item.DisplayName,
			Author:          item.Author,
			Homepage:        item.Homepage,
			Repository:      item.Repository,
			License:         item.License,
			Requirements:    requirements,
			Scope:           item.Scope,
			Path:            item.Path,
			Environment:     environmentItems,
			Resources: extensionResourceCounts{
				Skills:      item.Resources.Skills,
				MCPServers:  item.Resources.MCPServers,
				Hooks:       item.Resources.Hooks,
				Observables: item.Resources.Observables,
			},
		})
	}
	return extensionsStatus{Count: len(items), Items: items}
}

func runtimeToolsStatusFromApp(status app.RuntimeToolsStatus) runtimeToolsStatus {
	groups := make([]runtimeToolGroupInfo, 0, len(status.Groups))
	for _, group := range status.Groups {
		groups = append(groups, runtimeToolGroupInfo{
			Group: group.Group,
			Tools: runtimeToolInfosFromApp(group.Tools),
		})
	}
	return runtimeToolsStatus{Count: status.Count, Groups: groups}
}

func runtimeToolInfosFromApp(tools []app.RuntimeToolInfo) []runtimeToolInfo {
	infos := make([]runtimeToolInfo, 0, len(tools))
	for _, tool := range tools {
		infos = append(infos, runtimeToolInfo{
			Name:        tool.Name,
			Module:      tool.Module,
			Description: tool.Description,
			Schema:      tool.Schema,
			Timeout: runtimeToolTimeout{
				Mode:    tool.Timeout.Mode,
				Seconds: tool.Timeout.Seconds,
			},
		})
	}
	return infos
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
			Type:      server.Type,
			URL:       server.URL,
			Command:   server.Command,
			Args:      append([]string(nil), server.Args...),
			Status:    server.Status,
			Connected: server.Connected,
			ToolCount: server.ToolCount,
			Tools:     runtimeToolInfosFromApp(server.Tools),
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
			Required:       command.Required,
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

func (s *Server) loadMCPConfigs(runtime app.AgentRuntimeResolution) ([]mcp.Config, error) {
	return app.LoadMCPConfigs(runtime, s.absoluteWorkDir())
}
