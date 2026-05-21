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
	ProviderID           string
	ProviderProtocol     string
	BaseURL              string
	APIKey               string
	Model                string
	ThinkingEffort       string // "low", "medium", "high", or "" (provider default)
	ContextWindow        int    // provider context window in tokens; defaults to 256K
	ProviderHeaders      map[string]string
	ProviderQuery        map[string]string
	ProviderCapabilities llm.CapabilityOverrides
	ProviderCompat       llm.CompatOptions
	Compaction           CompactionConfig

	HomeAgentsDir string // ~/.agents (user-global)
	WorkDir       string // explicit; defaults to os.Getwd()
}

type fileConfig struct {
	Provider   providerConfig   `yaml:"provider"`
	Compaction compactionConfig `yaml:"compaction"`
}

type providerConfig struct {
	ID             string                     `yaml:"id"`
	Protocol       string                     `yaml:"protocol"`
	BaseURL        string                     `yaml:"base_url"`
	APIKey         string                     `yaml:"api_key"`
	Model          string                     `yaml:"model"`
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
	Enabled            bool
	ReserveTokens      int
	KeepRecentTokens   int
	TailTurns          int
	SummaryMaxTokens   int
	ToolResultMaxChars int
}

type compactionConfig struct {
	Enabled            *bool `yaml:"enabled"`
	ReserveTokens      int   `yaml:"reserve_tokens"`
	KeepRecentTokens   int   `yaml:"keep_recent_tokens"`
	TailTurns          int   `yaml:"tail_turns"`
	SummaryMaxTokens   int   `yaml:"summary_max_tokens"`
	ToolResultMaxChars int   `yaml:"tool_result_max_chars"`
}

const DefaultContextWindow = 256000

var providerEnvKeys = []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW"}

// Load resolves config from <WorkDir>/.juex/juex.yaml and OS env vars.
//
// Priority (later wins): defaults < <WorkDir>/.juex/juex.yaml < os.Environ.
func Load() (Config, error) {
	return LoadForWorkDir("")
}

// LoadForWorkDir is Load with an explicit working directory.
func LoadForWorkDir(workDir string) (Config, error) {
	return loadForWorkDir(workDir, true)
}

func loadForWorkDir(workDir string, resolveAuth bool) (Config, error) {
	cfg := Config{ContextWindow: DefaultContextWindow, Compaction: DefaultCompactionConfig()}

	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return cfg, err
		}
		workDir = cwd
	}
	cfg.WorkDir = workDir
	if home, err := os.UserHomeDir(); err == nil {
		cfg.HomeAgentsDir = filepath.Join(home, ".agents")
	}

	if err := applyYAMLFile(&cfg, cfg.RuntimeConfigPath(), true); err != nil {
		return cfg, err
	}
	applyOSEnv(&cfg)
	if resolveAuth {
		if err := resolveCodexAuth(&cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func resolveCodexAuthInConfig(cfg Config) (Config, error) {
	if err := resolveCodexAuth(&cfg); err != nil {
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
	var (
		cfg Config
		err error
	)
	if workDir != "" {
		cfg, err = loadForWorkDir(workDir, false)
	} else {
		cfg, err = loadForWorkDir("", false)
	}
	if err != nil {
		return cfg, err
	}
	err = applyYAMLFile(&cfg, path, false)
	if err != nil {
		return cfg, err
	}
	applyOSEnv(&cfg)
	return resolveCodexAuthInConfig(cfg)
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
	if c.HomeAgentsDir != "" {
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
	return filepath.Join(c.JuexDir(), "juex.yaml")
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
	if c.HomeAgentsDir != "" {
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
	applyProviderConfig(cfg, fc.Provider)
	applyCompactionConfig(cfg, fc.Compaction)
	return nil
}

func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Enabled:            true,
		ReserveTokens:      16384,
		KeepRecentTokens:   20000,
		TailTurns:          2,
		SummaryMaxTokens:   2048,
		ToolResultMaxChars: 2000,
	}
}

func applyProviderConfig(cfg *Config, p providerConfig) {
	if providerSelectorSpecified(p.ID, p.Protocol) {
		resetProviderConfig(cfg)
	}
	applyProviderSelectorConfig(cfg, p.ID, p.Protocol)
	if p.BaseURL != "" {
		cfg.BaseURL = p.BaseURL
	}
	if p.APIKey != "" {
		cfg.APIKey = p.APIKey
	}
	if p.Model != "" {
		cfg.Model = p.Model
	}
	if p.ThinkingEffort != "" {
		cfg.ThinkingEffort = p.ThinkingEffort
	}
	if p.ContextWindow > 0 {
		cfg.ContextWindow = p.ContextWindow
	}
	cfg.ProviderHeaders = mergeStringMap(cfg.ProviderHeaders, p.Headers)
	cfg.ProviderQuery = mergeStringMap(cfg.ProviderQuery, p.Query)
	applyProviderCapabilitiesConfig(&cfg.ProviderCapabilities, p.Capabilities)
	if len(p.Compat.ReasoningReplayFields) > 0 {
		cfg.ProviderCompat.ReasoningReplayFields = append([]string(nil), p.Compat.ReasoningReplayFields...)
	}
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
