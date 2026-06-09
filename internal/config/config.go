// Package config wires the runtime: config-file loading, agents-dir resolution,
// and LLM provider construction. Everything that needs a filesystem path lives
// here so other packages can stay path-agnostic.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
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
	ProviderHeaders           map[string]string
	ProviderQuery             map[string]string
	ProviderCapabilities      llm.CapabilityOverrides
	ProviderCompat            llm.CompatOptions
	Compaction                CompactionConfig
	Runtime                   RuntimeConfig
	Shell                     ShellProfile
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
	Runtime                   runtimeConfig    `yaml:"runtime"`
	Shell                     *ShellConfig     `yaml:"shell"`
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
	Streaming       *bool `yaml:"streaming"`
	ReasoningEffort *bool `yaml:"reasoning_effort"`
	ReasoningReplay *bool `yaml:"reasoning_replay"`
	MaxOutputTokens *bool `yaml:"max_output_tokens"`
}

type providerCompatConfig struct {
	ReasoningReplayFields []string `yaml:"reasoning_replay_fields"`
}

type CompactionConfig struct {
	Enabled                    bool
	ReserveTokens              int
	KeepRecentTokens           int
	TailTurns                  int
	SummaryMaxTokens           int
	ToolResultMaxChars         int
	UserInputInlineMaxBytes    int
	UserInputPreviewHeadBytes  int
	UserInputPreviewTailBytes  int
	ToolResultInlineMaxBytes   int
	ToolResultPreviewHeadBytes int
	ToolResultPreviewTailBytes int
	MaxAutoFailures            int
}

type RuntimeConfig struct {
	MaxIters    int
	MaxDuration time.Duration
}

type compactionConfig struct {
	Enabled                    *bool `yaml:"enabled"`
	ReserveTokens              int   `yaml:"reserve_tokens"`
	KeepRecentTokens           int   `yaml:"keep_recent_tokens"`
	TailTurns                  int   `yaml:"tail_turns"`
	SummaryMaxTokens           int   `yaml:"summary_max_tokens"`
	ToolResultMaxChars         int   `yaml:"tool_result_max_chars"`
	UserInputInlineMaxBytes    int   `yaml:"user_input_inline_max_bytes"`
	UserInputPreviewHeadBytes  int   `yaml:"user_input_preview_head_bytes"`
	UserInputPreviewTailBytes  int   `yaml:"user_input_preview_tail_bytes"`
	ToolResultInlineMaxBytes   int   `yaml:"tool_result_inline_max_bytes"`
	ToolResultPreviewHeadBytes int   `yaml:"tool_result_preview_head_bytes"`
	ToolResultPreviewTailBytes int   `yaml:"tool_result_preview_tail_bytes"`
	MaxAutoFailures            int   `yaml:"max_auto_failures"`
}

type runtimeConfig struct {
	MaxIters    optionalPositiveInt `yaml:"max_iters"`
	MaxDuration yamlDuration        `yaml:"max_duration"`
}

type optionalPositiveInt struct {
	Set   bool
	Value int
}

type yamlDuration struct {
	Set   bool
	Value time.Duration
}

const DefaultContextWindow = 256000

var providerEnvKeys = []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW"}

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
	cfg, err := loadForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := finalizeLoadedConfig(&cfg, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func loadForWorkDir(workDir string) (Config, error) {
	cfg := Config{
		ContextWindow:             DefaultContextWindow,
		Compaction:                DefaultCompactionConfig(),
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

	if err := applyYAMLFile(&cfg, cfg.HomeRuntimeConfigPath(), true); err != nil {
		return cfg, err
	}

	if err := applyYAMLFile(&cfg, cfg.RuntimeConfigPath(), true); err != nil {
		return cfg, err
	}
	if err := resolveSelectedProvider(&cfg); err != nil {
		return cfg, err
	}
	applyOSEnv(&cfg)
	return cfg, nil
}

// LoadFromFile is a convenience for tests / `juex run --config <path>`.
// It applies overrides from path on top of Load(); WorkDir is unaffected.
func LoadFromFile(path string) (Config, error) {
	return LoadFromFileForWorkDir(path, "")
}

// LoadFromFileForWorkDir is LoadFromFile with an explicit working directory.
func LoadFromFileForWorkDir(path, workDir string) (Config, error) {
	var (
		cfg Config
		err error
	)
	if workDir != "" {
		cfg, err = loadForWorkDir(workDir)
	} else {
		cfg, err = loadForWorkDir("")
	}
	if err != nil {
		return cfg, err
	}
	err = applyYAMLFile(&cfg, path, false)
	if err != nil {
		return cfg, err
	}
	if err := resolveSelectedProvider(&cfg); err != nil {
		return cfg, err
	}
	applyOSEnv(&cfg)
	if err := finalizeLoadedConfig(&cfg, true); err != nil {
		return cfg, err
	}
	return cfg, nil
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

// NewProvider constructs the LLM provider implied by the config.
func (c Config) NewProvider() (llm.Provider, error) {
	if c.ProviderID == "" && c.ProviderProtocol == "" {
		return nil, fmt.Errorf("config: provider id/protocol is empty")
	}
	return llm.New(c.llmConfig())
}

func (c Config) ProviderProfile() (llm.ProviderProfile, error) {
	return llm.ResolveProfile(c.llmConfig())
}

func (c Config) llmConfig() llm.Config {
	return llm.Config{
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

// ProjectAgentsDir is <WorkDir>/.agents.
func (c Config) ProjectAgentsDir() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.WorkDir, ".agents")
}

// JuexDir is <WorkDir>/.juex and stores runtime data.
func (c Config) JuexDir() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.WorkDir, ".juex")
}

// SkillDirs returns the skill directories in load order:
// user-global first, project-local second (project entries override
// user entries by name).
func (c Config) SkillDirs() []string {
	var out []string
	if c.EnableUserGlobalResources && c.HomeAgentsDir != "" {
		out = append(out, filepath.Join(c.HomeAgentsDir, "skills"))
	}
	if c.WorkDir != "" {
		out = append(out, filepath.Join(c.WorkDir, ".agents", "skills"))
	}
	return out
}

// MemoryDir returns the work-local memory store path.
func (c Config) MemoryDir() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.JuexDir(), "memory")
}

// SessionsDir returns the work-local sessions root.
func (c Config) SessionsDir() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.JuexDir(), "sessions")
}

// HistoryPath returns the work-local session history index path.
func (c Config) HistoryPath() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.JuexDir(), "history.json")
}

// RuntimeConfigPath returns the work-local runtime config file path.
func (c Config) RuntimeConfigPath() string {
	if c.WorkDir == "" {
		return ""
	}
	if filepath.Base(filepath.Clean(c.WorkDir)) == ".juex" {
		return filepath.Join(c.WorkDir, "juex.yaml")
	}
	return filepath.Join(c.JuexDir(), "juex.yaml")
}

// HomeRuntimeConfigPath returns the user-global runtime config path.
func (c Config) HomeRuntimeConfigPath() string {
	if c.HomeJuexDir == "" {
		return ""
	}
	return filepath.Join(c.HomeJuexDir, "juex.yaml")
}

// GlobalAgentsMDPath returns the user-global AGENTS.md path when user-global
// resources are enabled.
func (c Config) GlobalAgentsMDPath() string {
	if !c.EnableUserGlobalResources || c.HomeAgentsDir == "" {
		return ""
	}
	return filepath.Join(c.HomeAgentsDir, "AGENTS.md")
}

// AgentsMDDirs returns directories that may contain AGENTS.md (project root
// + project .agents subdir). The home-global AGENTS.md is loaded separately
// because its absolute path is required.
func (c Config) AgentsMDDirs() []string {
	if c.WorkDir == "" {
		return nil
	}
	return []string{c.WorkDir, filepath.Join(c.WorkDir, ".agents")}
}

// MCPConfigPaths returns mcp.json candidates in load order:
// user-global first, project-local second.
func (c Config) MCPConfigPaths() []string {
	var out []string
	if c.EnableUserGlobalResources && c.HomeAgentsDir != "" {
		out = append(out, filepath.Join(c.HomeAgentsDir, "mcp.json"))
	}
	if c.WorkDir != "" {
		out = append(out, filepath.Join(c.WorkDir, ".agents", "mcp.json"))
	}
	return out
}

func applyYAMLFile(cfg *Config, path string, missingOK bool) error {
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
	applyCompactionConfig(cfg, fc.Compaction)
	applyRuntimeConfig(cfg, fc.Runtime)
	if fc.Shell != nil {
		cfg.shellConfig = *fc.Shell
	}
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

func (d *yamlDuration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("expected duration scalar, got non-scalar node")
	}
	value := strings.TrimSpace(node.Value)
	if value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("expected duration like 30s or 5m, got %q", value)
	}
	if parsed <= 0 {
		return fmt.Errorf("duration must be positive, got %q", value)
	}
	d.Set = true
	d.Value = parsed
	return nil
}

func (i *optionalPositiveInt) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("expected positive integer scalar, got non-scalar node")
	}
	value, err := strconv.Atoi(strings.TrimSpace(node.Value))
	if err != nil {
		return fmt.Errorf("expected positive integer, got %q", node.Value)
	}
	if value <= 0 {
		return fmt.Errorf("integer must be positive, got %d", value)
	}
	i.Set = true
	i.Value = value
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

func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Enabled:                    true,
		ReserveTokens:              16384,
		KeepRecentTokens:           20000,
		TailTurns:                  2,
		SummaryMaxTokens:           2048,
		ToolResultMaxChars:         2000,
		UserInputInlineMaxBytes:    65536,
		UserInputPreviewHeadBytes:  8192,
		UserInputPreviewTailBytes:  8192,
		ToolResultInlineMaxBytes:   32768,
		ToolResultPreviewHeadBytes: 8192,
		ToolResultPreviewTailBytes: 8192,
		MaxAutoFailures:            3,
	}
}

func applyRuntimeConfig(cfg *Config, c runtimeConfig) {
	if c.MaxIters.Set {
		cfg.Runtime.MaxIters = c.MaxIters.Value
	}
	if c.MaxDuration.Set {
		cfg.Runtime.MaxDuration = c.MaxDuration.Value
	}
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
		p.ID = id
		for _, model := range p.Models {
			if strings.TrimSpace(model.ID) == "" {
				return fmt.Errorf("provider %q model id is required", id)
			}
		}
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
	return base
}

func mergeProviderCapabilitiesConfig(base, override providerCapabilitiesConfig) providerCapabilitiesConfig {
	if override.Tools != nil {
		base.Tools = override.Tools
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
	ref := strings.TrimSpace(cfg.modelRef)
	if ref == "" {
		return nil
	}
	providerID, modelID, err := parseModelRef(ref)
	if err != nil {
		return err
	}
	p, ok := cfg.providerConfigs[providerID]
	if !ok {
		return fmt.Errorf("config: model %q references unknown provider %q", ref, providerID)
	}
	model, ok := providerModelByID(p.Models, modelID)
	if !ok {
		return fmt.Errorf("config: model %q references unknown model %q for provider %q", ref, modelID, providerID)
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
	return nil
}

func parseModelRef(ref string) (string, string, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("config: model must be provider_id/model, got %q", ref)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
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
	if c.ReserveTokens > 0 {
		cfg.Compaction.ReserveTokens = c.ReserveTokens
	}
	if c.KeepRecentTokens > 0 {
		cfg.Compaction.KeepRecentTokens = c.KeepRecentTokens
	}
	if c.TailTurns > 0 {
		cfg.Compaction.TailTurns = c.TailTurns
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

func applyOSEnv(cfg *Config) {
	values := map[string]string{}
	for _, key := range providerEnvKeys {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			values[key] = v
		}
	}
	applyEnvMap(cfg, values)
}

func applyEnvMap(cfg *Config, values map[string]string) {
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
		cfg.ThinkingEffort = v
	}
	if v, ok := values["PROVIDER_CONTEXT_WINDOW"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ContextWindow = n
		}
	}
}

func applyProviderCapabilitiesConfig(dst *llm.CapabilityOverrides, src providerCapabilitiesConfig) {
	if src.Tools != nil {
		dst.Tools = src.Tools
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
