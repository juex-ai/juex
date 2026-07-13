// Package config resolves config files, env overrides, auth, and filesystem
// paths into explicit values for app/runtime composition.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"
	"github.com/juex-ai/juex/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// Config holds runtime-wide settings.
//
// HomeAgentsDir hosts user-global resources (AGENTS.md, skills, mcp.json).
// WorkDir hosts work-local resources. Project AGENTS.md, skills, and mcp.json
// live under .agents. Runtime data (memory, sessions, history) lives under
// .juex so it does not overlap with project agent configuration.
type Config struct {
	ProviderID                string
	ProviderProtocol          string
	BaseURL                   string
	APIKey                    string
	Model                     string
	ThinkingEffort            string // "low", "medium", "high", "xhigh", "max", or "" (provider default)
	ContextWindow             int    // provider context window in tokens; defaults to 256K
	MaxOutputTokens           int    // optional provider-visible output cap for normal turns
	ProviderHeaders           map[string]string
	ProviderQuery             map[string]string
	ProviderCapabilities      llm.CapabilityOverrides
	ProviderCompat            llm.CompatOptions
	Compaction                CompactionConfig
	PendingInputTTL           time.Duration
	ExternalEventTTL          time.Duration
	ToolTimeout               time.Duration
	ShowBuiltinHookTraces     bool
	Hooks                     hooks.Config
	Shell                     ShellProfile
	Sandbox                   sandbox.Policy
	Skills                    SkillsConfig
	EnableUserGlobalResources bool

	HomeAgentsDir string // ~/.agents (user-global)
	HomeJuexDir   string // ~/.juex (user-global runtime config)
	WorkDir       string // explicit; defaults to os.Getwd()

	modelRef        string
	shellConfig     ShellConfig
	providerConfigs map[string]providerConfig
}

type fileConfig struct {
	Model                     string           `yaml:"model"`
	EnableUserGlobalResources optionalBool     `yaml:"enable_user_global_resources"`
	Providers                 []providerConfig `yaml:"providers"`
	Compaction                compactionConfig `yaml:"compaction"`
	Hooks                     hooks.FileConfig `yaml:"hooks"`
	Runtime                   runtimeConfig    `yaml:"runtime"`
	Shell                     *ShellConfig     `yaml:"shell"`
	Sandbox                   sandboxConfig    `yaml:"sandbox"`
	Skills                    skillsConfig     `yaml:"skills"`
}

type providerConfig struct {
	ID           string                     `yaml:"id"`
	Protocol     string                     `yaml:"protocol"`
	BaseURL      string                     `yaml:"base_url"`
	APIKey       string                     `yaml:"api_key"`
	Headers      map[string]string          `yaml:"headers"`
	Query        map[string]string          `yaml:"query"`
	Capabilities providerCapabilitiesConfig `yaml:"capabilities"`
	Compat       providerCompatConfig       `yaml:"compat"`
	Models       []providerModelConfig      `yaml:"models"`
}

type providerModelConfig struct {
	ID             string                     `yaml:"id"`
	ThinkingEffort string                     `yaml:"thinking_effort"`
	ContextWindow  int                        `yaml:"context_window"`
	Headers        map[string]string          `yaml:"headers"`
	Query          map[string]string          `yaml:"query"`
	Capabilities   providerCapabilitiesConfig `yaml:"capabilities"`
	Compat         providerCompatConfig       `yaml:"compat"`
}

type providerCapabilitiesConfig struct {
	Tools           *bool `yaml:"tools"`
	Vision          *bool `yaml:"vision"`
	Streaming       *bool `yaml:"streaming"`
	ReasoningEffort *bool `yaml:"reasoning_effort"`
	ReasoningReplay *bool `yaml:"reasoning_replay"`
	MaxOutputTokens *bool `yaml:"max_output_tokens"`
}

type providerCompatConfig struct {
	ReasoningReplayFields []string `yaml:"reasoning_replay_fields"`
	CodexTransport        string   `yaml:"codex_transport"`
}

type CompactionConfig = runtimepolicy.CompactionPolicy

// ModelRef is the provider:model selector used by the top-level config model.
// The provider id may not contain ":", while the model id may contain slashes
// for OpenAI-compatible proxy model names such as meta-llama/Llama-3.
type ModelRef struct {
	ProviderID string
	ModelID    string
}

func ParseModelRef(ref string) (ModelRef, error) {
	parts := strings.SplitN(strings.TrimSpace(ref), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return ModelRef{}, fmt.Errorf("config: model must be provider:model, got %q", ref)
	}
	return ModelRef{ProviderID: strings.TrimSpace(parts[0]), ModelID: strings.TrimSpace(parts[1])}, nil
}

func (r ModelRef) String() string {
	if r.ProviderID == "" && r.ModelID == "" {
		return ""
	}
	return r.ProviderID + ":" + r.ModelID
}

// ApplyModelOverride selects a configured provider:model using the same
// provider:model grammar as the top-level YAML model field.
func (c *Config) ApplyModelOverride(ref string) error {
	trimmed := strings.TrimSpace(ref)
	modelRef, err := ParseModelRef(trimmed)
	if err != nil {
		return err
	}
	c.modelRef = trimmed
	return resolveSelectedProviderRef(c, modelRef)
}

// ModelOverrideError marks a failure caused by an explicit model override,
// allowing CLI callers to map it to usage errors without misclassifying
// unrelated config load failures.
type ModelOverrideError struct {
	Err error
}

func (e *ModelOverrideError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ModelOverrideError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type compactionConfig struct {
	Enabled                    *bool   `yaml:"enabled"`
	Instructions               *string `yaml:"instructions"`
	ReserveTokens              int     `yaml:"reserve_tokens"`
	KeepRecentTokens           int     `yaml:"keep_recent_tokens"`
	TailTurns                  int     `yaml:"tail_turns"`
	SummaryModel               string  `yaml:"summary_model"`
	SummaryMaxTokens           int     `yaml:"summary_max_tokens"`
	ToolResultMaxChars         int     `yaml:"tool_result_max_chars"`
	UserInputInlineMaxBytes    int     `yaml:"user_input_inline_max_bytes"`
	UserInputPreviewHeadBytes  int     `yaml:"user_input_preview_head_bytes"`
	UserInputPreviewTailBytes  int     `yaml:"user_input_preview_tail_bytes"`
	ToolResultInlineMaxBytes   int     `yaml:"tool_result_inline_max_bytes"`
	ToolResultPreviewHeadBytes int     `yaml:"tool_result_preview_head_bytes"`
	ToolResultPreviewTailBytes int     `yaml:"tool_result_preview_tail_bytes"`
	MaxAutoFailures            int     `yaml:"max_auto_failures"`
}

type runtimeConfig struct {
	PendingInputTTL          time.Duration
	PendingInputTTLSet       bool
	ExternalEventTTL         time.Duration
	ExternalEventTTLSet      bool
	ToolTimeout              time.Duration
	ToolTimeoutSet           bool
	MaxOutputTokens          int
	MaxOutputTokensSet       bool
	ShowBuiltinHookTraces    bool
	ShowBuiltinHookTracesSet bool
}

type sandboxConfig struct {
	Enabled    optionalBool            `yaml:"enabled"`
	FileSystem sandboxFileSystemConfig `yaml:"file_system"`
	Network    sandboxNetworkConfig    `yaml:"network"`
}

type sandboxFileSystemConfig struct {
	OutsideWorkspace string   `yaml:"outside_workspace"`
	BlockedPaths     []string `yaml:"blocked_paths"`
}

type sandboxNetworkConfig struct {
	Enabled optionalBool `yaml:"enabled"`
}

type SkillsConfig struct {
	Include           []string
	Exclude           []string
	PromptBudgetChars int
}

type skillsConfig struct {
	Include           *[]string `yaml:"include"`
	Exclude           *[]string `yaml:"exclude"`
	PromptBudgetChars int       `yaml:"prompt_budget_chars"`
}

func (c *runtimeConfig) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind == 0 || node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("runtime must be a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := strings.TrimSpace(node.Content[i].Value)
		value := node.Content[i+1]
		switch key {
		case "pending_input_ttl":
			d, err := parseRuntimeDuration(key, value)
			if err != nil {
				return err
			}
			c.PendingInputTTL = d
			c.PendingInputTTLSet = true
		case "external_event_ttl":
			d, err := parseRuntimeDuration(key, value)
			if err != nil {
				return err
			}
			c.ExternalEventTTL = d
			c.ExternalEventTTLSet = true
		case "tool_timeout":
			d, err := parseRuntimeDuration(key, value)
			if err != nil {
				return err
			}
			c.ToolTimeout = d
			c.ToolTimeoutSet = true
		case "max_output_tokens":
			n, err := parseRuntimePositiveInt(key, value)
			if err != nil {
				return err
			}
			c.MaxOutputTokens = n
			c.MaxOutputTokensSet = true
		case "show_builtin_hook_traces":
			enabled, err := ParseBoolValue(value.Value)
			if err != nil {
				return fmt.Errorf("runtime.%s: %w", key, err)
			}
			c.ShowBuiltinHookTraces = enabled
			c.ShowBuiltinHookTracesSet = true
		default:
			// Legacy runtime budget keys were accepted before runtime had
			// configurable policy; keep ignoring unknown runtime keys so old
			// workspace configs do not fail to load.
		}
	}
	return nil
}

const DefaultContextWindow = runtimepolicy.DefaultContextWindowTokens
const DefaultPendingInputTTL = 15 * time.Minute
const DefaultExternalEventTTL = 24 * time.Hour
const DefaultToolTimeout = 60 * time.Second
const DefaultSkillPromptBudgetChars = 8000

var providerEnvKeys = []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW"}

var allowedThinkingEfforts = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
	"max":    {},
}

const allowedThinkingEffortText = "low, medium, high, xhigh, max"

// Load resolves config from ~/.juex/juex.yaml, the work-local juex.yaml, and
// OS env vars.
//
// Priority (later wins): defaults < ~/.juex/juex.yaml <
// <WorkDir>/.juex/juex.yaml (or <WorkDir>/juex.yaml when WorkDir is .juex) <
// os.Environ.
func Load() (Config, error) {
	return LoadForWorkDir("")
}

// LoadForWorkDir is Load with an explicit working directory.
func LoadForWorkDir(workDir string) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoad(&cfg, "", true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadForWorkDirWithModelOverride is LoadForWorkDir with an explicit model
// selector that wins over YAML and PROVIDER_API_MODEL.
func LoadForWorkDirWithModelOverride(workDir, modelRef string) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoad(&cfg, modelRef, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func loadConfigFilesForWorkDir(workDir string) (Config, error) {
	cfg := Config{
		ContextWindow:             DefaultContextWindow,
		Compaction:                DefaultCompactionConfig(),
		PendingInputTTL:           DefaultPendingInputTTL,
		ExternalEventTTL:          DefaultExternalEventTTL,
		ToolTimeout:               DefaultToolTimeout,
		Sandbox:                   sandbox.DefaultPolicy(),
		Skills:                    DefaultSkillsConfig(),
		EnableUserGlobalResources: true,
		providerConfigs:           map[string]providerConfig{},
	}

	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return cfg, err
		}
		workDir = cwd
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	cfg.WorkDir = workDir
	if home, err := os.UserHomeDir(); err == nil {
		cfg.HomeAgentsDir = filepath.Join(home, ".agents")
		cfg.HomeJuexDir = filepath.Join(home, ".juex")
	}

	if err := applyYAMLFile(&cfg, cfg.HomeRuntimeConfigPath(), true, "user", false); err != nil {
		return cfg, err
	}

	if err := applyYAMLFile(&cfg, cfg.RuntimeConfigPath(), true, "project", true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadFromFile is a convenience for tests / `juex run --config <path>`.
// It applies overrides from path on top of Load(); WorkDir is unaffected.
func LoadFromFile(path string) (Config, error) {
	return LoadFromFileForWorkDir(path, "")
}

// LoadFromFileForWorkDir is LoadFromFile with an explicit working directory.
func LoadFromFileForWorkDir(path, workDir string) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	err = applyYAMLFile(&cfg, path, false, "project", true)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoad(&cfg, "", true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadFromFileForWorkDirWithModelOverride is LoadFromFileForWorkDir with an
// explicit model selector that wins over YAML and PROVIDER_API_MODEL.
func LoadFromFileForWorkDirWithModelOverride(path, workDir, modelRef string) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	err = applyYAMLFile(&cfg, path, false, "project", true)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoad(&cfg, modelRef, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func finalizeConfigLoad(cfg *Config, modelRef string, resolveAuth bool) error {
	if strings.TrimSpace(modelRef) != "" {
		if err := cfg.ApplyModelOverride(modelRef); err != nil {
			return &ModelOverrideError{Err: err}
		}
		if err := applyOSEnvExcept(cfg, map[string]struct{}{
			"PROVIDER_API_ID":       {},
			"PROVIDER_API_PROTOCOL": {},
			"PROVIDER_API_MODEL":    {},
		}); err != nil {
			return err
		}
		return finalizeLoadedConfig(cfg, resolveAuth)
	}
	if err := resolveSelectedProvider(cfg); err != nil {
		return err
	}
	if err := applyOSEnv(cfg); err != nil {
		return err
	}
	return finalizeLoadedConfig(cfg, resolveAuth)
}

func finalizeLoadedConfig(cfg *Config, resolveAuth bool) error {
	if err := resolveShellProfileForConfig(cfg); err != nil {
		return err
	}
	if resolveAuth {
		if err := resolveCodexAuth(cfg); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) ProviderProfile() (llm.ProviderProfile, error) {
	return c.ProviderSelection().ProviderProfile()
}

// ProjectAgentsDir is <WorkDir>/.agents.
func (c Config) ProjectAgentsDir() string {
	return c.ResourcePaths().ProjectAgentsDir
}

// HomeExtensionsDir is ~/.juex/extensions when user-global resources are enabled.
func (c Config) HomeExtensionsDir() string {
	return c.ResourcePaths().HomeExtensionsDir
}

// ProjectExtensionsDir is <WorkDir>/.juex/extensions.
func (c Config) ProjectExtensionsDir() string {
	return c.ResourcePaths().ProjectExtensionsDir
}

// JuexDir is <WorkDir>/.juex and stores runtime data.
func (c Config) JuexDir() string {
	return c.RuntimePaths().JuexDir
}

// SkillDirs returns the skill directories in load order:
// user-global first, project-local second (project entries override
// user entries by name).
func (c Config) SkillDirs() []string {
	return c.ResourcePaths().SkillDirs
}

// MemoryDir returns the work-local memory store path.
func (c Config) MemoryDir() string {
	return c.RuntimePaths().MemoryDir
}

// SessionsDir returns the work-local sessions root.
func (c Config) SessionsDir() string {
	return c.RuntimePaths().SessionsDir
}

// HistoryPath returns the work-local session history index path.
func (c Config) HistoryPath() string {
	return c.RuntimePaths().HistoryPath
}

// RuntimeConfigPath returns the work-local runtime config file path.
func (c Config) RuntimeConfigPath() string {
	return c.RuntimePaths().RuntimeConfigPath
}

// HomeRuntimeConfigPath returns the user-global runtime config path.
func (c Config) HomeRuntimeConfigPath() string {
	return c.RuntimePaths().HomeRuntimeConfigPath
}

// GlobalAgentsMDPath returns the user-global AGENTS.md path when user-global
// resources are enabled.
func (c Config) GlobalAgentsMDPath() string {
	return c.ResourcePaths().GlobalAgentsMDPath
}

// AgentsMDDirs returns directories that may contain AGENTS.md (project root
// + project .agents subdir). The home-global AGENTS.md is loaded separately
// because its absolute path is required.
func (c Config) AgentsMDDirs() []string {
	return c.ResourcePaths().AgentsMDDirs
}

// MCPConfigPaths returns mcp.json candidates in load order:
// user-global first, project-local second.
func (c Config) MCPConfigPaths() []string {
	return c.ResourcePaths().MCPConfigPaths
}

func applyYAMLFile(cfg *Config, path string, missingOK bool, hookSource string, requireHookTrust bool) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if missingOK && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	if strings.TrimSpace(fc.Model) != "" {
		cfg.modelRef = strings.TrimSpace(fc.Model)
	}
	if fc.EnableUserGlobalResources.Set {
		cfg.EnableUserGlobalResources = fc.EnableUserGlobalResources.Value
	}
	if err := applyProvidersConfig(cfg, fc.Providers); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := applyHooksConfig(cfg, fc.Hooks, hookSource, requireHookTrust); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	applyCompactionConfig(cfg, fc.Compaction)
	applyRuntimeConfig(cfg, fc.Runtime)
	if err := applySkillsConfig(cfg, fc.Skills); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := applySandboxConfig(cfg, fc.Sandbox); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	if fc.Shell != nil {
		cfg.shellConfig = *fc.Shell
	}
	return nil
}

func applyHooksConfig(cfg *Config, fileHooks hooks.FileConfig, source string, requireTrust bool) error {
	resolved, err := hooks.ResolveFileConfig(fileHooks, source, requireTrust)
	if err != nil {
		return err
	}
	cfg.Hooks.Commands = append(cfg.Hooks.Commands, resolved.Commands...)
	return nil
}

type optionalBool struct {
	Set   bool
	Value bool
}

func (b *optionalBool) UnmarshalYAML(node *yaml.Node) error {
	value, err := ParseBoolValue(node.Value)
	if err != nil {
		return err
	}
	b.Set = true
	b.Value = value
	return nil
}

// ParseBoolValue parses config/flag boolean values. It accepts true/false,
// 1/0, yes/no, and on/off so CLI and YAML behave the same way.
func ParseBoolValue(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean value true/false or 1/0, got %q", value)
	}
}

func parseRuntimeDuration(field string, node *yaml.Node) (time.Duration, error) {
	if node == nil || node.Tag == "!!null" {
		return 0, nil
	}
	if node.Kind != yaml.ScalarNode {
		return 0, fmt.Errorf("runtime.%s must be a duration string", field)
	}
	value := strings.TrimSpace(node.Value)
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("runtime.%s: %w", field, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("runtime.%s must be positive", field)
	}
	return d, nil
}

func parseRuntimePositiveInt(field string, node *yaml.Node) (int, error) {
	if node == nil || node.Tag == "!!null" {
		return 0, nil
	}
	if node.Kind != yaml.ScalarNode {
		return 0, fmt.Errorf("runtime.%s must be a positive integer", field)
	}
	value := strings.TrimSpace(node.Value)
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("runtime.%s: %w", field, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("runtime.%s must be positive", field)
	}
	return n, nil
}

func DefaultSkillsConfig() SkillsConfig {
	return SkillsConfig{PromptBudgetChars: DefaultSkillPromptBudgetChars}
}

func applySkillsConfig(cfg *Config, fileSkills skillsConfig) error {
	if fileSkills.Include != nil {
		cfg.Skills.Include = cleanStringList(*fileSkills.Include)
	}
	if fileSkills.Exclude != nil {
		cfg.Skills.Exclude = cleanStringList(*fileSkills.Exclude)
	}
	if fileSkills.PromptBudgetChars < 0 {
		return fmt.Errorf("skills.prompt_budget_chars must be non-negative")
	}
	if fileSkills.PromptBudgetChars > 0 {
		cfg.Skills.PromptBudgetChars = fileSkills.PromptBudgetChars
	}
	return nil
}

func cleanStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func DefaultCompactionConfig() CompactionConfig {
	return runtimepolicy.DefaultCompactionPolicy()
}

func applyProvidersConfig(cfg *Config, providers []providerConfig) error {
	if len(providers) == 0 {
		return nil
	}
	if cfg.providerConfigs == nil {
		cfg.providerConfigs = map[string]providerConfig{}
	}
	for _, p := range providers {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			return fmt.Errorf("provider id is required")
		}
		if strings.Contains(id, ":") {
			return fmt.Errorf("provider %q id must not contain ':'", id)
		}
		p.ID = id
		for i := range p.Models {
			model := &p.Models[i]
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				return fmt.Errorf("provider %q model id is required", id)
			}
			model.ID = modelID
			thinkingEffort, err := normalizeThinkingEffort(model.ThinkingEffort)
			if err != nil {
				return fmt.Errorf("provider %q model %q: %w", id, modelID, err)
			}
			model.ThinkingEffort = thinkingEffort
			codexTransport, err := llm.NormalizeCodexTransport(model.Compat.CodexTransport)
			if err != nil {
				return fmt.Errorf("provider %q model %q: %w", id, modelID, err)
			}
			model.Compat.CodexTransport = codexTransport
		}
		codexTransport, err := llm.NormalizeCodexTransport(p.Compat.CodexTransport)
		if err != nil {
			return fmt.Errorf("provider %q: %w", id, err)
		}
		p.Compat.CodexTransport = codexTransport
		existing := cfg.providerConfigs[id]
		cfg.providerConfigs[id] = mergeProviderConfig(existing, p)
	}
	return nil
}

func mergeProviderConfig(base, override providerConfig) providerConfig {
	if strings.TrimSpace(override.ID) != "" {
		base.ID = strings.TrimSpace(override.ID)
	}
	if strings.TrimSpace(override.Protocol) != "" {
		base.Protocol = strings.TrimSpace(override.Protocol)
	}
	if override.BaseURL != "" {
		base.BaseURL = override.BaseURL
	}
	if override.APIKey != "" {
		base.APIKey = override.APIKey
	}
	base.Headers = mergeStringMap(base.Headers, override.Headers)
	base.Query = mergeStringMap(base.Query, override.Query)
	base.Capabilities = mergeProviderCapabilitiesConfig(base.Capabilities, override.Capabilities)
	if len(override.Compat.ReasoningReplayFields) > 0 {
		base.Compat.ReasoningReplayFields = append([]string(nil), override.Compat.ReasoningReplayFields...)
	}
	if override.Compat.CodexTransport != "" {
		base.Compat.CodexTransport = override.Compat.CodexTransport
	}
	base.Models = mergeProviderModelConfigs(base.Models, override.Models)
	return base
}

func mergeProviderModelConfigs(base, overrides []providerModelConfig) []providerModelConfig {
	if len(overrides) == 0 {
		return base
	}
	out := append([]providerModelConfig(nil), base...)
	for _, override := range overrides {
		id := strings.TrimSpace(override.ID)
		if id == "" {
			continue
		}
		override.ID = id
		idx := -1
		for i := range out {
			if out[i].ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			out = append(out, override)
			continue
		}
		out[idx] = mergeProviderModelConfig(out[idx], override)
	}
	return out
}

func mergeProviderModelConfig(base, override providerModelConfig) providerModelConfig {
	if strings.TrimSpace(override.ID) != "" {
		base.ID = strings.TrimSpace(override.ID)
	}
	if override.ThinkingEffort != "" {
		base.ThinkingEffort = override.ThinkingEffort
	}
	if override.ContextWindow > 0 {
		base.ContextWindow = override.ContextWindow
	}
	base.Headers = mergeStringMap(base.Headers, override.Headers)
	base.Query = mergeStringMap(base.Query, override.Query)
	base.Capabilities = mergeProviderCapabilitiesConfig(base.Capabilities, override.Capabilities)
	if len(override.Compat.ReasoningReplayFields) > 0 {
		base.Compat.ReasoningReplayFields = append([]string(nil), override.Compat.ReasoningReplayFields...)
	}
	if override.Compat.CodexTransport != "" {
		base.Compat.CodexTransport = override.Compat.CodexTransport
	}
	return base
}

func mergeProviderCapabilitiesConfig(base, override providerCapabilitiesConfig) providerCapabilitiesConfig {
	if override.Tools != nil {
		base.Tools = override.Tools
	}
	if override.Vision != nil {
		base.Vision = override.Vision
	}
	if override.Streaming != nil {
		base.Streaming = override.Streaming
	}
	if override.ReasoningEffort != nil {
		base.ReasoningEffort = override.ReasoningEffort
	}
	if override.ReasoningReplay != nil {
		base.ReasoningReplay = override.ReasoningReplay
	}
	if override.MaxOutputTokens != nil {
		base.MaxOutputTokens = override.MaxOutputTokens
	}
	return base
}

func resolveSelectedProvider(cfg *Config) error {
	rawRef := strings.TrimSpace(cfg.modelRef)
	if rawRef == "" {
		return nil
	}
	ref, err := ParseModelRef(rawRef)
	if err != nil {
		return err
	}
	return resolveSelectedProviderRef(cfg, ref)
}

func resolveSelectedProviderRef(cfg *Config, ref ModelRef) error {
	p, ok := cfg.providerConfigs[ref.ProviderID]
	if !ok {
		return fmt.Errorf("config: model %q references unknown provider %q", ref.String(), ref.ProviderID)
	}
	model, ok := providerModelByID(p.Models, ref.ModelID)
	if !ok {
		return fmt.Errorf("config: model %q references unknown model %q for provider %q", ref.String(), ref.ModelID, ref.ProviderID)
	}
	resetProviderConfig(cfg)
	cfg.ProviderID = p.ID
	cfg.ProviderProtocol = p.Protocol
	cfg.BaseURL = p.BaseURL
	cfg.APIKey = p.APIKey
	cfg.Model = model.ID
	cfg.ProviderHeaders = mergeStringMap(cfg.ProviderHeaders, p.Headers)
	cfg.ProviderQuery = mergeStringMap(cfg.ProviderQuery, p.Query)
	applyProviderCapabilitiesConfig(&cfg.ProviderCapabilities, p.Capabilities)
	if len(p.Compat.ReasoningReplayFields) > 0 {
		cfg.ProviderCompat.ReasoningReplayFields = append([]string(nil), p.Compat.ReasoningReplayFields...)
	}
	if p.Compat.CodexTransport != "" {
		cfg.ProviderCompat.CodexTransport = p.Compat.CodexTransport
	}
	if model.ThinkingEffort != "" {
		cfg.ThinkingEffort = model.ThinkingEffort
	}
	if model.ContextWindow > 0 {
		cfg.ContextWindow = model.ContextWindow
	}
	cfg.ProviderHeaders = mergeStringMap(cfg.ProviderHeaders, model.Headers)
	cfg.ProviderQuery = mergeStringMap(cfg.ProviderQuery, model.Query)
	applyProviderCapabilitiesConfig(&cfg.ProviderCapabilities, model.Capabilities)
	if len(model.Compat.ReasoningReplayFields) > 0 {
		cfg.ProviderCompat.ReasoningReplayFields = append([]string(nil), model.Compat.ReasoningReplayFields...)
	}
	if model.Compat.CodexTransport != "" {
		cfg.ProviderCompat.CodexTransport = model.Compat.CodexTransport
	}
	return nil
}

func providerModelByID(models []providerModelConfig, id string) (providerModelConfig, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return providerModelConfig{}, false
}

func providerSelectorSpecified(id, protocol string) bool {
	return id != "" || protocol != ""
}

func resetProviderConfig(cfg *Config) {
	cfg.ProviderID = ""
	cfg.ProviderProtocol = ""
	cfg.BaseURL = ""
	cfg.APIKey = ""
	cfg.Model = ""
	cfg.ThinkingEffort = ""
	cfg.ContextWindow = DefaultContextWindow
	cfg.ProviderHeaders = nil
	cfg.ProviderQuery = nil
	cfg.ProviderCapabilities = llm.CapabilityOverrides{}
	cfg.ProviderCompat = llm.CompatOptions{}
}

func applyProviderSelectorConfig(cfg *Config, id, protocol string) {
	if !providerSelectorSpecified(id, protocol) {
		return
	}
	cfg.ProviderID = id
	cfg.ProviderProtocol = protocol
}

func applyCompactionConfig(cfg *Config, c compactionConfig) {
	if c.Enabled != nil {
		cfg.Compaction.Enabled = *c.Enabled
	}
	if c.Instructions != nil {
		cfg.Compaction.Instructions = strings.TrimSpace(*c.Instructions)
	}
	if c.ReserveTokens > 0 {
		cfg.Compaction.ReserveTokens = c.ReserveTokens
	}
	if c.KeepRecentTokens > 0 {
		cfg.Compaction.KeepRecentTokens = c.KeepRecentTokens
	}
	if c.TailTurns > 0 {
		cfg.Compaction.TailTurns = c.TailTurns
	}
	if strings.TrimSpace(c.SummaryModel) != "" {
		cfg.Compaction.SummaryModel = strings.TrimSpace(c.SummaryModel)
	}
	if c.SummaryMaxTokens > 0 {
		cfg.Compaction.SummaryMaxTokens = c.SummaryMaxTokens
	}
	if c.ToolResultMaxChars > 0 {
		cfg.Compaction.ToolResultMaxChars = c.ToolResultMaxChars
	}
	if c.UserInputInlineMaxBytes > 0 {
		cfg.Compaction.UserInputInlineMaxBytes = c.UserInputInlineMaxBytes
	}
	if c.UserInputPreviewHeadBytes > 0 {
		cfg.Compaction.UserInputPreviewHeadBytes = c.UserInputPreviewHeadBytes
	}
	if c.UserInputPreviewTailBytes > 0 {
		cfg.Compaction.UserInputPreviewTailBytes = c.UserInputPreviewTailBytes
	}
	if c.ToolResultInlineMaxBytes > 0 {
		cfg.Compaction.ToolResultInlineMaxBytes = c.ToolResultInlineMaxBytes
	}
	if c.ToolResultPreviewHeadBytes > 0 {
		cfg.Compaction.ToolResultPreviewHeadBytes = c.ToolResultPreviewHeadBytes
	}
	if c.ToolResultPreviewTailBytes > 0 {
		cfg.Compaction.ToolResultPreviewTailBytes = c.ToolResultPreviewTailBytes
	}
	if c.MaxAutoFailures > 0 {
		cfg.Compaction.MaxAutoFailures = c.MaxAutoFailures
	}
}

func applyRuntimeConfig(cfg *Config, c runtimeConfig) {
	if c.PendingInputTTLSet {
		cfg.PendingInputTTL = c.PendingInputTTL
	}
	if c.ExternalEventTTLSet {
		cfg.ExternalEventTTL = c.ExternalEventTTL
	}
	if c.ToolTimeoutSet {
		cfg.ToolTimeout = c.ToolTimeout
	}
	if c.MaxOutputTokensSet {
		cfg.MaxOutputTokens = c.MaxOutputTokens
	}
	if c.ShowBuiltinHookTracesSet {
		cfg.ShowBuiltinHookTraces = c.ShowBuiltinHookTraces
	}
}

func applySandboxConfig(cfg *Config, c sandboxConfig) error {
	if c.Enabled.Set {
		cfg.Sandbox.Enabled = c.Enabled.Value
	}
	if strings.TrimSpace(c.FileSystem.OutsideWorkspace) != "" {
		access := sandbox.OutsideWorkspaceAccess(strings.TrimSpace(c.FileSystem.OutsideWorkspace))
		if err := sandbox.ValidateOutsideWorkspaceAccess(access); err != nil {
			return err
		}
		cfg.Sandbox.FileSystem.OutsideWorkspace = access
	}
	if len(c.FileSystem.BlockedPaths) > 0 {
		paths, err := sandbox.AppendBlockedPaths(cfg.Sandbox.FileSystem.BlockedPaths, c.FileSystem.BlockedPaths)
		if err != nil {
			return err
		}
		cfg.Sandbox.FileSystem.BlockedPaths = paths
	}
	if c.Network.Enabled.Set {
		cfg.Sandbox.Network.Enabled = c.Network.Enabled.Value
	}
	return nil
}

func normalizeThinkingEffort(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if _, ok := allowedThinkingEfforts[trimmed]; ok {
		return trimmed, nil
	}
	return "", fmt.Errorf("invalid thinking_effort %q (allowed values: %s)", value, allowedThinkingEffortText)
}

func applyOSEnv(cfg *Config) error {
	return applyOSEnvExcept(cfg, nil)
}

func applyOSEnvExcept(cfg *Config, excluded map[string]struct{}) error {
	values := map[string]string{}
	for _, key := range providerEnvKeys {
		if _, skip := excluded[key]; skip {
			continue
		}
		if v, ok := os.LookupEnv(key); ok && v != "" {
			values[key] = v
		}
	}
	return applyEnvMap(cfg, values)
}

func applyEnvMap(cfg *Config, values map[string]string) error {
	id, hasID := values["PROVIDER_API_ID"]
	protocol, hasProtocol := values["PROVIDER_API_PROTOCOL"]
	if hasID || hasProtocol {
		applyProviderSelectorConfig(cfg, id, protocol)
	}
	if v, ok := values["PROVIDER_API_BASE"]; ok && v != "" {
		cfg.BaseURL = v
	}
	if v, ok := values["PROVIDER_API_KEY"]; ok && v != "" {
		cfg.APIKey = v
	}
	if v, ok := values["PROVIDER_API_MODEL"]; ok && v != "" {
		cfg.Model = v
	}
	if v, ok := values["PROVIDER_THINKING_EFFORT"]; ok && v != "" {
		thinkingEffort, err := normalizeThinkingEffort(v)
		if err != nil {
			return fmt.Errorf("PROVIDER_THINKING_EFFORT: %w", err)
		}
		if thinkingEffort != "" {
			cfg.ThinkingEffort = thinkingEffort
		}
	}
	if v, ok := values["PROVIDER_CONTEXT_WINDOW"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ContextWindow = n
		}
	}
	return nil
}

func applyProviderCapabilitiesConfig(dst *llm.CapabilityOverrides, src providerCapabilitiesConfig) {
	if src.Tools != nil {
		dst.Tools = src.Tools
	}
	if src.Vision != nil {
		dst.Vision = src.Vision
	}
	if src.Streaming != nil {
		dst.Streaming = src.Streaming
	}
	if src.ReasoningEffort != nil {
		dst.ReasoningEffort = src.ReasoningEffort
	}
	if src.ReasoningReplay != nil {
		dst.ReasoningReplay = src.ReasoningReplay
	}
	if src.MaxOutputTokens != nil {
		dst.MaxOutputTokens = src.MaxOutputTokens
	}
}

func mergeStringMap(base, override map[string]string) map[string]string {
	if len(override) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		if v == "" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}
