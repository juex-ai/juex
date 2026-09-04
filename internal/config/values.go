package config

import (
	"fmt"
	"path/filepath"
	"runtime"
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
	MediaDir       string
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
	}
}

func (c Config) ProviderSelectionForModelRef(ref string) (ProviderSelection, error) {
	resolved, err := c.ResolvedModelForRef(ref)
	if err != nil {
		return ProviderSelection{}, err
	}
	return resolved.Selection, nil
}

// ResolvedModelForRef returns the effective provider selection and model
// limits for a model ref that is not necessarily part of the serving chain.
func (c Config) ResolvedModelForRef(ref string) (ResolvedModel, error) {
	canonical, err := ParseModelRef(ref)
	if err != nil {
		return ResolvedModel{}, err
	}
	cfg, err := c.configForModelRef(canonical.String())
	if err != nil {
		return ResolvedModel{}, err
	}
	return ResolvedModel{
		Ref:             canonical.String(),
		Selection:       cfg.ProviderSelection(),
		ContextWindow:   cfg.ContextWindow,
		MaxOutputTokens: cfg.MaxOutputTokens,
	}, nil
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
	if err := finalizeLoadedConfig(&cfg, true, true); err != nil {
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
	configuredTail := c.Models
	if len(configuredTail) > 0 {
		configuredTail = configuredTail[1:]
	}
	for _, ref := range configuredTail {
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
		MediaDir:       s.MediaDir,
	}
}

// RuntimePaths separates workspace-local configuration from identity-owned
// runtime state.
type RuntimePaths struct {
	WorkDir               string
	JuexDir               string
	StateDir              string
	MediaDir              string
	ThreadsDir            string
	ThreadIndexPath       string
	WorkspaceConfigPath   string
	DefaultHomeConfigPath string
	HomeConfigPath        string
	AgentConfigPath       string
}

func (c Config) RuntimePaths() RuntimePaths {
	paths := RuntimePaths{WorkDir: c.WorkDir}
	if c.WorkDir != "" {
		paths.JuexDir = filepath.Join(c.WorkDir, ".juex")
		paths.StateDir = c.AgentStateDir
		if paths.StateDir == "" && !c.agentStateLoaded {
			// Keep manually constructed Config values useful to isolated
			// package tests and embedding callers that do not load config.
			paths.StateDir = paths.JuexDir
		}
		paths.ThreadsDir = filepath.Join(paths.StateDir, "threads")
		paths.ThreadIndexPath = filepath.Join(paths.StateDir, "threads.index.json")
		if c.AgentStateDir != "" {
			paths.MediaDir = filepath.Join(c.AgentStateDir, "media")
			paths.AgentConfigPath = filepath.Join(c.AgentStateDir, "juex.yaml")
		}
		if filepath.Base(filepath.Clean(c.WorkDir)) == ".juex" {
			paths.WorkspaceConfigPath = filepath.Join(c.WorkDir, "juex.yaml")
		} else {
			paths.WorkspaceConfigPath = filepath.Join(paths.JuexDir, "juex.yaml")
		}
	}
	if c.HomeJuexDir != "" {
		paths.HomeConfigPath = filepath.Join(c.HomeJuexDir, "juex.yaml")
	}
	paths.DefaultHomeConfigPath = c.defaultHomeConfigPath
	return paths
}

func (c Config) ObservablesConfigPath() string {
	stateDir := c.RuntimePaths().StateDir
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "observables.json")
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
	WorkDir                  string
	HomeAgentsDir            string
	DefaultHomeExtensionsDir string
	HomeExtensionsDir        string
	ProjectAgentsDir         string
	ProjectExtensionsDir     string
	GlobalAgentsMDPath       string
	SkillDirs                []string
	AgentsMDDirs             []string
	MCPConfigPaths           []string
	UserAgentsResources      bool
}

func (c Config) ResourcePaths() ResourcePaths {
	paths := ResourcePaths{
		WorkDir:             c.WorkDir,
		HomeAgentsDir:       c.HomeAgentsDir,
		UserAgentsResources: c.EnableUserAgentsResources,
	}
	if c.EnableUserAgentsResources && c.HomeAgentsDir != "" {
		paths.GlobalAgentsMDPath = filepath.Join(c.HomeAgentsDir, "AGENTS.md")
		paths.SkillDirs = append(paths.SkillDirs, filepath.Join(c.HomeAgentsDir, "skills"))
		paths.MCPConfigPaths = append(paths.MCPConfigPaths, filepath.Join(c.HomeAgentsDir, "mcp.json"))
	}
	if c.HomeJuexDir != "" {
		paths.HomeExtensionsDir = filepath.Join(c.HomeJuexDir, "extensions")
	}
	if c.defaultHomeConfigPath != "" {
		paths.DefaultHomeExtensionsDir = filepath.Join(filepath.Dir(c.defaultHomeConfigPath), "extensions")
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

func (c Config) ExtensionPolicy() ExtensionPolicy {
	if !c.Extensions.Configured {
		return ExtensionPolicy{}
	}
	return ExtensionPolicy{
		Allow:      append([]string(nil), c.Extensions.Allow...),
		Configured: c.Extensions.Configured,
	}
}

// RuntimeLimits contains runtime policy values after config resolution.
type RuntimeLimits struct {
	ContextWindow           int
	MaxOutputTokens         int
	Compaction              CompactionConfig
	ToolOutput              ToolOutputConfig
	PendingInputTTL         time.Duration
	ExternalEventTTL        time.Duration
	ToolTimeout             time.Duration
	ShowBuiltinPolicyTraces bool
	NotifyModelChanges      bool
}

func (c Config) RuntimeLimits() RuntimeLimits {
	return RuntimeLimits{
		ContextWindow:           c.ContextWindow,
		MaxOutputTokens:         c.MaxOutputTokens,
		Compaction:              c.Compaction,
		ToolOutput:              c.ToolOutput,
		PendingInputTTL:         c.PendingInputTTL,
		ExternalEventTTL:        c.ExternalEventTTL,
		ToolTimeout:             c.ToolTimeout,
		ShowBuiltinPolicyTraces: c.ShowBuiltinPolicyTraces,
		NotifyModelChanges:      c.NotifyModelChanges,
	}
}

func (c Config) SandboxPolicy() sandbox.Policy {
	return c.SandboxPolicyForOS(runtime.GOOS)
}

func (c Config) SandboxPolicyForOS(runtimeOS string) sandbox.Policy {
	if !c.sandboxConfigured && isZeroSandboxPolicy(c.Sandbox) {
		return sandbox.DefaultPolicyForOS(runtimeOS)
	}
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
