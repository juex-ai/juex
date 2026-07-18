package config

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/sandbox"
)

type SandboxPolicy = sandbox.Policy
type FileSystemSandboxPolicy = sandbox.FileSystemPolicy
type OutsideWorkspaceAccess = sandbox.OutsideWorkspaceAccess
type NetworkSandboxPolicy = sandbox.NetworkPolicy

const (
	OutsideWorkspaceReadWrite OutsideWorkspaceAccess = sandbox.OutsideWorkspaceReadWrite
	OutsideWorkspaceReadOnly  OutsideWorkspaceAccess = sandbox.OutsideWorkspaceReadOnly
)

type SkillPolicy struct {
	Include           []string
	Exclude           []string
	PromptBudgetChars int
}

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
	WorkDir        string
}

// ResolvedModel is one effective primary or fallback model after config and
// environment resolution. Ref is the canonical provider:model identity used
// for health and transcript attribution.
type ResolvedModel struct {
	Ref             string
	Selection       ProviderSelection
	ContextWindow   int
	MaxOutputTokens int
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
		WorkDir:        c.WorkDir,
	}
}

func (c Config) ProviderSelectionForModelRef(ref string) (ProviderSelection, error) {
	cfg, err := c.configForModelRef(ref)
	if err != nil {
		return ProviderSelection{}, err
	}
	return cfg.ProviderSelection(), nil
}

func (c Config) configForModelRef(ref string) (Config, error) {
	cfg := c
	if err := cfg.ApplyModelOverride(ref); err != nil {
		return Config{}, err
	}
	if err := applyOSEnvExcept(&cfg, map[string]struct{}{
		"PROVIDER_API_ID":       {},
		"PROVIDER_API_PROTOCOL": {},
		"PROVIDER_API_MODEL":    {},
	}); err != nil {
		return Config{}, err
	}
	if err := finalizeLoadedConfig(&cfg, true, false); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) ModelChain() ([]ResolvedModel, error) {
	primaryRef := ModelRef{ProviderID: c.ProviderID, ModelID: c.Model}.String()
	if primaryRef == "" {
		return nil, fmt.Errorf("config: effective primary model is empty")
	}
	chain := []ResolvedModel{{
		Ref:             primaryRef,
		Selection:       c.ProviderSelection(),
		ContextWindow:   c.ContextWindow,
		MaxOutputTokens: c.MaxOutputTokens,
	}}
	seen := map[string]struct{}{primaryRef: {}}
	for _, ref := range c.FallbackModels {
		canonical, err := ParseModelRef(ref)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[canonical.String()]; duplicate {
			continue
		}
		resolved, err := c.configForModelRef(canonical.String())
		if err != nil {
			return nil, err
		}
		chain = append(chain, ResolvedModel{
			Ref:             canonical.String(),
			Selection:       resolved.ProviderSelection(),
			ContextWindow:   resolved.ContextWindow,
			MaxOutputTokens: resolved.MaxOutputTokens,
		})
		seen[canonical.String()] = struct{}{}
	}
	return chain, nil
}

func (c Config) ProviderProfileForModelRef(ref string) (llm.ProviderProfile, error) {
	selection, err := c.ProviderSelectionForModelRef(ref)
	if err != nil {
		return llm.ProviderProfile{}, err
	}
	return selection.ProviderProfile()
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
		WorkDir:        s.WorkDir,
	}
}

// RuntimePaths separates workspace-local configuration from identity-owned
// runtime state.
type RuntimePaths struct {
	WorkDir               string
	JuexDir               string
	StateDir              string
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
		paths.StateDir = c.AgentStateDir
		if paths.StateDir == "" {
			// Keep manually constructed Config values useful to isolated
			// package tests and embedding callers that do not load config.
			paths.StateDir = paths.JuexDir
		}
		paths.MemoryDir = filepath.Join(paths.StateDir, "memory")
		paths.SessionsDir = filepath.Join(paths.StateDir, "sessions")
		paths.HistoryPath = filepath.Join(paths.StateDir, "history.json")
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

func (c Config) ObservablesConfigPath() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.WorkDir, ".juex", "observables.json")
}

func (c Config) ObservablesStateDir() string {
	stateDir := c.RuntimePaths().StateDir
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "observables")
}

// ResourcePaths contains AGENTS, skill, MCP, and extension resource locations.
type ResourcePaths struct {
	WorkDir              string
	HomeAgentsDir        string
	HomeExtensionsDir    string
	ProjectAgentsDir     string
	ProjectExtensionsDir string
	GlobalAgentsMDPath   string
	SkillDirs            []string
	AgentsMDDirs         []string
	MCPConfigPaths       []string
	UserGlobalResources  bool
}

func (c Config) ResourcePaths() ResourcePaths {
	paths := ResourcePaths{
		WorkDir:             c.WorkDir,
		HomeAgentsDir:       c.HomeAgentsDir,
		UserGlobalResources: c.EnableUserGlobalResources,
	}
	if c.EnableUserGlobalResources && c.HomeAgentsDir != "" {
		paths.GlobalAgentsMDPath = filepath.Join(c.HomeAgentsDir, "AGENTS.md")
		paths.SkillDirs = append(paths.SkillDirs, filepath.Join(c.HomeAgentsDir, "skills"))
		paths.MCPConfigPaths = append(paths.MCPConfigPaths, filepath.Join(c.HomeAgentsDir, "mcp.json"))
	}
	if c.EnableUserGlobalResources && c.HomeJuexDir != "" {
		paths.HomeExtensionsDir = filepath.Join(c.HomeJuexDir, "extensions")
	}
	if c.WorkDir != "" {
		paths.ProjectAgentsDir = filepath.Join(c.WorkDir, ".agents")
		paths.ProjectExtensionsDir = filepath.Join(c.WorkDir, ".juex", "extensions")
		paths.SkillDirs = append(paths.SkillDirs, filepath.Join(paths.ProjectAgentsDir, "skills"))
		paths.AgentsMDDirs = []string{c.WorkDir, paths.ProjectAgentsDir}
		paths.MCPConfigPaths = append(paths.MCPConfigPaths, filepath.Join(paths.ProjectAgentsDir, "mcp.json"))
	}
	return paths
}

func (c Config) SkillPolicy() SkillPolicy {
	policy := SkillPolicy{
		Include:           append([]string(nil), c.Skills.Include...),
		Exclude:           append([]string(nil), c.Skills.Exclude...),
		PromptBudgetChars: c.Skills.PromptBudgetChars,
	}
	if policy.PromptBudgetChars <= 0 {
		policy.PromptBudgetChars = DefaultSkillPromptBudgetChars
	}
	if c.ContextWindow > 0 {
		contextBudget := c.ContextWindow * 2 / 100 * 4
		if contextBudget > 0 && contextBudget < policy.PromptBudgetChars {
			policy.PromptBudgetChars = contextBudget
		}
	}
	return policy
}

// RuntimeLimits contains runtime policy values after config resolution.
type RuntimeLimits struct {
	ContextWindow         int
	MaxOutputTokens       int
	Compaction            CompactionConfig
	PendingInputTTL       time.Duration
	ExternalEventTTL      time.Duration
	ToolTimeout           time.Duration
	ShowBuiltinHookTraces bool
}

func (c Config) RuntimeLimits() RuntimeLimits {
	return RuntimeLimits{
		ContextWindow:         c.ContextWindow,
		MaxOutputTokens:       c.MaxOutputTokens,
		Compaction:            c.Compaction,
		PendingInputTTL:       c.PendingInputTTL,
		ExternalEventTTL:      c.ExternalEventTTL,
		ToolTimeout:           c.ToolTimeout,
		ShowBuiltinHookTraces: c.ShowBuiltinHookTraces,
	}
}

func (c Config) SandboxPolicy() sandbox.Policy {
	policy := c.Sandbox
	if policy.FileSystem.OutsideWorkspace == "" {
		policy.FileSystem.OutsideWorkspace = sandbox.OutsideWorkspaceReadWrite
	}
	if isZeroSandboxPolicy(c.Sandbox) {
		policy.Network.Enabled = true
	}
	return policy
}

func isZeroSandboxPolicy(policy sandbox.Policy) bool {
	return !policy.Enabled &&
		policy.FileSystem.OutsideWorkspace == "" &&
		len(policy.FileSystem.BlockedPaths) == 0 &&
		!policy.Network.Enabled
}
