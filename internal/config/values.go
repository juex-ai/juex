package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

// ProviderSelection is the resolved provider/model value passed to the LLM
// boundary. It contains no provider construction behavior.
type ProviderSelection struct {
	ID             string
	Protocol       string
	BaseURL        string
	APIKey         string
	Model          string
	ThinkingEffort string
	Headers        map[string]string
	Query          map[string]string
	Capabilities   llm.CapabilityOverrides
	Compat         llm.CompatOptions
}

func (c Config) ProviderSelection() ProviderSelection {
	return ProviderSelection{
		ID:             c.ProviderID,
		Protocol:       c.ProviderProtocol,
		BaseURL:        c.BaseURL,
		APIKey:         c.APIKey,
		Model:          c.Model,
		ThinkingEffort: c.ThinkingEffort,
		Headers:        c.ProviderHeaders,
		Query:          c.ProviderQuery,
		Capabilities:   c.ProviderCapabilities,
		Compat:         c.ProviderCompat,
	}
}

func (s ProviderSelection) ProviderProfile() (llm.ProviderProfile, error) {
	if s.ID == "" && s.Protocol == "" {
		return llm.ProviderProfile{}, fmt.Errorf("config: provider id/protocol is empty")
	}
	return llm.ResolveProfile(s.llmConfig())
}

func (s ProviderSelection) llmConfig() llm.Config {
	return llm.Config{
		ID:             s.ID,
		Protocol:       s.Protocol,
		BaseURL:        s.BaseURL,
		APIKey:         s.APIKey,
		Model:          s.Model,
		ThinkingEffort: s.ThinkingEffort,
		Headers:        s.Headers,
		Query:          s.Query,
		Capabilities:   s.Capabilities,
		Compat:         s.Compat,
	}
}

// RuntimePaths contains work-local runtime storage paths plus the user-global
// runtime config path used during config loading.
type RuntimePaths struct {
	WorkDir               string
	JuexDir               string
	MemoryDir             string
	SessionsDir           string
	HistoryPath           string
	RuntimeConfigPath     string
	HomeRuntimeConfigPath string
}

func (c Config) RuntimePaths() RuntimePaths {
	paths := RuntimePaths{WorkDir: c.WorkDir}
	if c.WorkDir != "" {
		paths.JuexDir = filepath.Join(c.WorkDir, ".juex")
		paths.MemoryDir = filepath.Join(paths.JuexDir, "memory")
		paths.SessionsDir = filepath.Join(paths.JuexDir, "sessions")
		paths.HistoryPath = filepath.Join(paths.JuexDir, "history.json")
		if filepath.Base(filepath.Clean(c.WorkDir)) == ".juex" {
			paths.RuntimeConfigPath = filepath.Join(c.WorkDir, "juex.yaml")
		} else {
			paths.RuntimeConfigPath = filepath.Join(paths.JuexDir, "juex.yaml")
		}
	}
	if c.HomeJuexDir != "" {
		paths.HomeRuntimeConfigPath = filepath.Join(c.HomeJuexDir, "juex.yaml")
	}
	return paths
}

// ResourcePaths contains AGENTS, skill, and MCP resource locations.
type ResourcePaths struct {
	WorkDir             string
	HomeAgentsDir       string
	ProjectAgentsDir    string
	GlobalAgentsMDPath  string
	SkillDirs           []string
	AgentsMDDirs        []string
	MCPConfigPaths      []string
	UserGlobalResources bool
}

func (c Config) ResourcePaths() ResourcePaths {
	paths := ResourcePaths{
		WorkDir:             c.WorkDir,
		HomeAgentsDir:       c.HomeAgentsDir,
		UserGlobalResources: c.EnableUserGlobalResources,
	}
	if c.WorkDir != "" {
		paths.ProjectAgentsDir = filepath.Join(c.WorkDir, ".agents")
		paths.SkillDirs = append(paths.SkillDirs, filepath.Join(paths.ProjectAgentsDir, "skills"))
		paths.AgentsMDDirs = append(paths.AgentsMDDirs, c.WorkDir, paths.ProjectAgentsDir)
		paths.MCPConfigPaths = append(paths.MCPConfigPaths, filepath.Join(paths.ProjectAgentsDir, "mcp.json"))
	}
	if c.EnableUserGlobalResources && c.HomeAgentsDir != "" {
		paths.GlobalAgentsMDPath = filepath.Join(c.HomeAgentsDir, "AGENTS.md")
		paths.SkillDirs = append([]string{filepath.Join(c.HomeAgentsDir, "skills")}, paths.SkillDirs...)
		paths.MCPConfigPaths = append([]string{filepath.Join(c.HomeAgentsDir, "mcp.json")}, paths.MCPConfigPaths...)
	}
	return paths
}

// RuntimeLimits contains runtime policy values after config resolution.
type RuntimeLimits struct {
	MaxIters      int
	MaxDuration   time.Duration
	ContextWindow int
	Compaction    CompactionConfig
}

func (c Config) RuntimeLimits() RuntimeLimits {
	return RuntimeLimits{
		MaxIters:      c.Runtime.MaxIters,
		MaxDuration:   c.Runtime.MaxDuration,
		ContextWindow: c.ContextWindow,
		Compaction:    c.Compaction,
	}
}
