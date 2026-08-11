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

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"
	"github.com/juex-ai/juex/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// Config holds runtime-wide settings.
//
// HomeAgentsDir hosts user-global resources (AGENTS.md, skills, mcp.json).
// HomeJuexDir is the effective instance home and owns all writable state.
// Runtime config may inherit from the read-only default-home config path.
// WorkDir hosts work-local resources. Project AGENTS.md, skills, and mcp.json
// live under .agents. Agent-owned runtime data lives under AgentStateDir.
type Config struct {
	ProviderID                string
	ProviderProtocol          string
	BaseURL                   string
	APIKey                    string
	Model                     string
	FallbackModels            []string
	ThinkingEffort            string // "low", "medium", "high", "xhigh", "max", or "" (provider default)
	ContextWindow             int    // provider context window in tokens; defaults to 256K
	MaxOutputTokens           int    // optional provider-visible output cap for normal turns
	ProviderHeaders           map[string]string
	ProviderQuery             map[string]string
	ProviderCapabilities      llm.CapabilityOverrides
	ProviderCompat            llm.CompatOptions
	Compaction                CompactionConfig
	ToolOutput                ToolOutputConfig
	PendingInputTTL           time.Duration
	ExternalEventTTL          time.Duration
	ToolTimeout               time.Duration
	ShowBuiltinHookTraces     bool
	Hooks                     hooks.Config
	Shell                     ShellProfile
	Sandbox                   sandbox.Policy
	Skills                    SkillsConfig
	Extensions                ExtensionPolicy
	Fleet                     FleetConfig
	EnableUserAgentsResources bool

	HomeAgentsDir     string // ~/.agents (user-global resources)
	HomeJuexDir       string // effective $JUEX_HOME instance root and only write target
	WorkDir           string // explicit; defaults to os.Getwd()
	AgentID           string
	AgentName         string
	AgentStateDir     string
	AgentAddress      agentstate.AgentAddress
	AgentStateNotices []string
	agentStateLoaded  bool

	modelRef                     string
	shellConfig                  ShellConfig
	providerConfigs              map[string]providerConfig
	defaultHomeRuntimeConfigPath string

	loadDotenv         bool
	environmentLayers  []environment.Layer
	runtimeEnvironment environment.Snapshot
	launchEnvironment  environment.Snapshot
	runtimeEnvStatus   EnvironmentStatus
}

type AgentStateMode uint8

const (
	AgentStateMint AgentStateMode = iota
	AgentStateExisting
	AgentStateNone
)

type LoadOptions struct {
	WorkDir    string
	ConfigPath string
	ModelRef   string
	AgentState AgentStateMode
}

// EnvironmentStatus contains value-free diagnostics for the runtime
// environment resolved during config loading.
type EnvironmentStatus struct {
	DotenvPath          string
	DotenvEnabled       bool
	DotenvLoaded        bool
	ConfiguredVariables int
}

type fileConfig struct {
	Model                     string            `yaml:"model"`
	FallbackModels            *[]string         `yaml:"fallback_models"`
	EnableUserAgentsResources optionalBool      `yaml:"enable_user_agents_resources"`
	Providers                 []providerConfig  `yaml:"providers"`
	Compaction                compactionConfig  `yaml:"compaction"`
	ToolOutput                toolOutputConfig  `yaml:"tool_output"`
	Hooks                     hooks.FileConfig  `yaml:"hooks"`
	Runtime                   runtimeConfig     `yaml:"runtime"`
	Shell                     *ShellConfig      `yaml:"shell"`
	Sandbox                   sandboxConfig     `yaml:"sandbox"`
	Skills                    skillsConfig      `yaml:"skills"`
	Extensions                extensionsConfig  `yaml:"extensions"`
	Fleet                     *fleetFileConfig  `yaml:"fleet"`
	Environment               environmentConfig `yaml:"environment"`
}

type environmentConfig struct {
	LoadDotenv optionalBool      `yaml:"load_dotenv"`
	Variables  map[string]string `yaml:"variables"`
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
type ToolOutputConfig = runtimepolicy.ToolOutputPolicy

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
	Enabled                   *bool   `yaml:"enabled"`
	Instructions              *string `yaml:"instructions"`
	ReserveTokens             int     `yaml:"reserve_tokens"`
	KeepRecentTokens          int     `yaml:"keep_recent_tokens"`
	SummaryModel              string  `yaml:"summary_model"`
	SummaryMaxTokens          int     `yaml:"summary_max_tokens"`
	ToolResultMaxChars        int     `yaml:"tool_result_max_chars"`
	UserInputInlineMaxBytes   int     `yaml:"user_input_inline_max_bytes"`
	UserInputPreviewHeadBytes int     `yaml:"user_input_preview_head_bytes"`
	UserInputPreviewTailBytes int     `yaml:"user_input_preview_tail_bytes"`
	MaxAutoFailures           int     `yaml:"max_auto_failures"`
}

type toolOutputConfig struct {
	InlineMaxBytes   int `yaml:"inline_max_bytes"`
	PreviewHeadBytes int `yaml:"preview_head_bytes"`
	PreviewTailBytes int `yaml:"preview_tail_bytes"`
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

// ExtensionPolicy is the effective logical-name allowlist after durable config
// layers have been resolved.
type ExtensionPolicy struct {
	Allow      []string
	Configured bool
}

type extensionsConfig struct {
	Allow *[]string `yaml:"allow"`
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
			return fmt.Errorf("field runtime.%s not found", key)
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

// Load resolves config from the default home, an optional non-default
// JUEX_HOME, the work-local juex.yaml, <WorkDir>/.env when enabled, and the
// inherited process environment.
//
// YAML priority (later wins): defaults < ~/.juex/juex.yaml <
// $JUEX_HOME/juex.yaml when distinct < <WorkDir>/.juex/juex.yaml (or
// <WorkDir>/juex.yaml when WorkDir is .juex).
// Runtime-environment priority is documented by internal/environment and the
// architecture guide; config loading itself does not mutate os.Environ.
func Load() (Config, error) {
	return LoadForWorkDir("")
}

// LoadWithOptions loads runtime configuration with an explicit workspace
// identity policy. AgentStateMint preserves the historical loader behavior.
func LoadWithOptions(opts LoadOptions) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(opts.WorkDir)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(opts.ConfigPath) != "" {
		if err := applyExplicitYAMLFile(&cfg, opts.ConfigPath); err != nil {
			return cfg, err
		}
	}
	if err := finalizeConfigLoadWithAgentState(&cfg, opts.ModelRef, true, opts.AgentState); err != nil {
		return cfg, err
	}
	return cfg, nil
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

// LoadForWorkDirForValidation loads and validates runtime configuration
// without resolving or creating a workspace agent identity.
func LoadForWorkDirForValidation(workDir string) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoadForValidation(&cfg, "", true); err != nil {
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
	cfg, err := loadUserConfigForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := applyYAMLFile(&cfg, workspaceYAMLSource(cfg.RuntimeConfigPath())); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func loadUserConfigForWorkDir(workDir string) (Config, error) {
	cfg := Config{
		ContextWindow:             DefaultContextWindow,
		Compaction:                DefaultCompactionConfig(),
		ToolOutput:                DefaultToolOutputConfig(),
		PendingInputTTL:           DefaultPendingInputTTL,
		ExternalEventTTL:          DefaultExternalEventTTL,
		ToolTimeout:               DefaultToolTimeout,
		Sandbox:                   sandbox.DefaultPolicy(),
		Skills:                    DefaultSkillsConfig(),
		Fleet:                     FleetConfig{Addr: DefaultFleetAddr},
		EnableUserAgentsResources: true,
		providerConfigs:           map[string]providerConfig{},
		loadDotenv:                true,
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
	}
	homeConfig, err := resolveHomeConfigSources()
	if err != nil {
		return cfg, err
	}
	cfg.HomeJuexDir = homeConfig.EffectiveHomeDir
	cfg.defaultHomeRuntimeConfigPath = homeConfig.DefaultConfigPath

	for _, source := range homeConfig.Sources {
		if err := applyYAMLFile(&cfg, source); err != nil {
			return cfg, err
		}
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
	err = applyExplicitYAMLFile(&cfg, path)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoad(&cfg, "", true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadFromFileForWorkDirForValidation is LoadFromFileForWorkDir without
// resolving or creating a workspace agent identity.
func LoadFromFileForWorkDirForValidation(path, workDir string) (Config, error) {
	cfg, err := loadConfigFilesForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := applyExplicitYAMLFile(&cfg, path); err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoadForValidation(&cfg, "", true); err != nil {
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
	err = applyExplicitYAMLFile(&cfg, path)
	if err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoad(&cfg, modelRef, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func finalizeConfigLoad(cfg *Config, modelRef string, resolveAuth bool) error {
	return finalizeConfigLoadWithAgentState(cfg, modelRef, resolveAuth, AgentStateMint)
}

func finalizeConfigLoadForValidation(cfg *Config, modelRef string, resolveAuth bool) error {
	return finalizeConfigLoadWithAgentState(cfg, modelRef, resolveAuth, AgentStateNone)
}

func finalizeConfigLoadWithAgentState(
	cfg *Config,
	modelRef string,
	resolveAuth bool,
	agentStateMode AgentStateMode,
) (loadErr error) {
	if err := resolveRuntimeEnvironment(cfg); err != nil {
		return err
	}
	defer func() {
		loadErr = redactConfiguredEnvironmentError(cfg.EnvironmentSnapshot(), loadErr)
	}()
	if err := validateConfiguredFallbackModels(cfg); err != nil {
		return err
	}
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
		return finalizeLoadedConfig(cfg, resolveAuth, agentStateMode)
	}
	if err := resolveSelectedProvider(cfg); err != nil {
		return err
	}
	if err := applyOSEnv(cfg); err != nil {
		return err
	}
	return finalizeLoadedConfig(cfg, resolveAuth, agentStateMode)
}

type configuredEnvironmentError struct {
	message string
	err     error
}

func (e *configuredEnvironmentError) Error() string {
	return e.message
}

func (e *configuredEnvironmentError) Unwrap() error {
	return e.err
}

func redactConfiguredEnvironmentError(snapshot environment.Snapshot, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted, changed := snapshot.RedactConfiguredValues([]byte(message))
	if !changed {
		return err
	}
	return &configuredEnvironmentError{
		message: string(redacted),
		err:     err,
	}
}

func finalizeLoadedConfig(cfg *Config, resolveAuth bool, agentStateMode AgentStateMode) error {
	if err := resolveShellProfileForConfig(cfg); err != nil {
		return err
	}
	if resolveAuth {
		if err := resolveCodexAuth(cfg); err != nil {
			return err
		}
	}
	cfg.agentStateLoaded = true
	if agentStateMode == AgentStateNone {
		return nil
	}
	var (
		resolution agentstate.Resolution
		err        error
	)
	switch agentStateMode {
	case AgentStateMint:
		resolution, err = agentstate.Resolve(agentstate.Options{
			HomeDir: cfg.HomeJuexDir,
			WorkDir: cfg.WorkDir,
		})
	case AgentStateExisting:
		resolution, err = agentstate.ResolveExisting(agentstate.Options{
			HomeDir: cfg.HomeJuexDir,
			WorkDir: cfg.WorkDir,
		})
	default:
		return fmt.Errorf("config: unsupported agent state mode %d", agentStateMode)
	}
	if err != nil {
		return err
	}
	cfg.AgentID = resolution.Agent.ID
	cfg.AgentName = resolution.Agent.Name
	cfg.AgentStateDir = resolution.Address.StateDir()
	cfg.AgentAddress = resolution.Address
	cfg.AgentStateNotices = append([]string(nil), resolution.Notices...)
	return nil
}

// EffectiveHomeDir returns JUEX_HOME when configured, otherwise ~/.juex.
func EffectiveHomeDir() (string, error) {
	return agentstate.EffectiveHome()
}

func (c Config) ProviderProfile() (llm.ProviderProfile, error) {
	return c.ProviderSelection().ProviderProfile()
}

// ProjectAgentsDir is <WorkDir>/.agents.
func (c Config) ProjectAgentsDir() string {
	return c.ResourcePaths().ProjectAgentsDir
}

// HomeExtensionsDir is $JUEX_HOME/extensions.
func (c Config) HomeExtensionsDir() string {
	return c.ResourcePaths().HomeExtensionsDir
}

// ProjectExtensionsDir is <WorkDir>/.juex/extensions.
func (c Config) ProjectExtensionsDir() string {
	return c.ResourcePaths().ProjectExtensionsDir
}

// JuexDir is <WorkDir>/.juex and stores workspace-local JueX configuration.
func (c Config) JuexDir() string {
	return c.RuntimePaths().JuexDir
}

// SkillDirs returns the skill directories in load order:
// user-global first, project-local second (project entries override
// user entries by name).
func (c Config) SkillDirs() []string {
	return c.ResourcePaths().SkillDirs
}

// MemoryDir returns the resolved agent memory store path.
func (c Config) MemoryDir() string {
	return c.RuntimePaths().MemoryDir
}

// SessionsDir returns the resolved agent sessions root.
func (c Config) SessionsDir() string {
	return c.RuntimePaths().SessionsDir
}

// HistoryPath returns the resolved agent session history index path.
func (c Config) HistoryPath() string {
	return c.RuntimePaths().HistoryPath
}

// RuntimeConfigPath returns the work-local runtime config file path.
func (c Config) RuntimeConfigPath() string {
	return c.RuntimePaths().RuntimeConfigPath
}

// HomeRuntimeConfigPath returns the effective instance runtime config path.
func (c Config) HomeRuntimeConfigPath() string {
	return c.RuntimePaths().HomeRuntimeConfigPath
}

// DefaultHomeRuntimeConfigPath returns the shared default-home config path.
func (c Config) DefaultHomeRuntimeConfigPath() string {
	return c.RuntimePaths().DefaultHomeRuntimeConfigPath
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

// EnvironmentSnapshot returns the immutable effective environment resolved for
// this config. Manually constructed Config values fall back to the current
// inherited environment for compatibility with package-level tests.
func (c Config) EnvironmentSnapshot() environment.Snapshot {
	if c.runtimeEnvironment.IsZero() {
		return environment.FromEnviron(os.Environ())
	}
	return c.runtimeEnvironment
}

// LaunchEnvironmentSnapshot is the inherited process environment captured
// before workspace values are applied. It is reserved for trusted bootstrap
// helpers such as sandbox backends.
func (c Config) LaunchEnvironmentSnapshot() environment.Snapshot {
	if c.launchEnvironment.IsZero() {
		return environment.FromEnviron(os.Environ())
	}
	return c.launchEnvironment
}

func (c Config) EnvironmentStatus() EnvironmentStatus {
	return c.runtimeEnvStatus
}

func applyYAMLFile(cfg *Config, source yamlConfigSource) error {
	if source.Path == "" {
		return nil
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		if source.MissingOK && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return applyYAMLData(cfg, data, source)
}

func applyExplicitYAMLFile(cfg *Config, path string) error {
	// A CLI may point --config at a durable file that was already loaded.
	// Reapply its ordinary explicit overrides while preserving the Extension
	// allowlist at the file's durable scope.
	loadedPaths := []string{
		cfg.DefaultHomeRuntimeConfigPath(),
		cfg.HomeRuntimeConfigPath(),
		cfg.RuntimeConfigPath(),
	}
	for _, loadedPath := range loadedPaths {
		if loadedPath == "" {
			continue
		}
		sameLoadedFile, err := sameConfigPath(path, loadedPath)
		if err != nil {
			return err
		}
		if sameLoadedFile {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return applyYAMLDataWithOptions(cfg, data, explicitYAMLSource(path), applyYAMLDataOptions{
				SkipExtensionPolicy: true,
			})
		}
	}
	return applyYAMLFile(cfg, explicitYAMLSource(path))
}

func applyYAMLData(cfg *Config, data []byte, source yamlConfigSource) error {
	return applyYAMLDataWithOptions(cfg, data, source, applyYAMLDataOptions{})
}

type applyYAMLDataOptions struct {
	SkipExtensionPolicy bool
}

func applyYAMLDataWithOptions(cfg *Config, data []byte, source yamlConfigSource, opts applyYAMLDataOptions) error {
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return fmt.Errorf("config: parse %s: %w", source.Path, err)
	}
	if strings.TrimSpace(fc.Model) != "" {
		cfg.modelRef = strings.TrimSpace(fc.Model)
	}
	if fc.FallbackModels != nil {
		cfg.FallbackModels = append([]string(nil), (*fc.FallbackModels)...)
	}
	if fc.EnableUserAgentsResources.Set {
		cfg.EnableUserAgentsResources = fc.EnableUserAgentsResources.Value
	}
	if fc.Environment.LoadDotenv.Set {
		cfg.loadDotenv = fc.Environment.LoadDotenv.Value
	}
	if fc.Environment.Variables != nil {
		cfg.environmentLayers = append(cfg.environmentLayers, environment.Layer{
			Source: source.environmentSource(),
			Path:   source.Path,
			Values: cloneEnvironmentVariables(fc.Environment.Variables),
			Strict: true,
		})
	}
	if err := applyProvidersConfig(cfg, fc.Providers); err != nil {
		return fmt.Errorf("config: parse %s: %w", source.Path, err)
	}
	if err := applyHooksConfig(cfg, fc.Hooks, source.hookSource(), source.requireHookTrust()); err != nil {
		return fmt.Errorf("config: parse %s: %w", source.Path, err)
	}
	applyCompactionConfig(cfg, fc.Compaction)
	applyToolOutputConfig(cfg, fc.ToolOutput)
	applyRuntimeConfig(cfg, fc.Runtime)
	if err := applySkillsConfig(cfg, fc.Skills); err != nil {
		return fmt.Errorf("config: parse %s: %w", source.Path, err)
	}
	if !opts.SkipExtensionPolicy {
		if fc.Extensions.Allow != nil && !source.allowsExtensionPolicy() {
			return fmt.Errorf(
				"config: parse %s: extensions.allow is only supported in default Home, instance Home, or workspace config",
				source.Path,
			)
		}
		if err := applyExtensionsConfig(cfg, fc.Extensions); err != nil {
			return fmt.Errorf("config: parse %s: %w", source.Path, err)
		}
	}
	if err := applySandboxConfig(cfg, fc.Sandbox); err != nil {
		return fmt.Errorf("config: parse %s: %w", source.Path, err)
	}
	if fc.Shell != nil {
		cfg.shellConfig = *fc.Shell
	}
	if fc.Fleet != nil {
		if !source.allowsFleet() {
			return fmt.Errorf("config: parse %s: fleet is only supported in default or instance JueX Home config", source.Path)
		}
		addr := strings.TrimSpace(fc.Fleet.Addr)
		if addr != "" {
			if err := ValidateStableFleetAddr(addr); err != nil {
				return fmt.Errorf("config: parse %s: fleet.addr: %w", source.Path, err)
			}
			cfg.Fleet.Addr = addr
			cfg.Fleet.AddrConfigured = true
		}
		if fc.Fleet.UnsafeBindAny.Set {
			cfg.Fleet.UnsafeBindAny = fc.Fleet.UnsafeBindAny.Value
		}
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

func applyExtensionsConfig(cfg *Config, fileExtensions extensionsConfig) error {
	if fileExtensions.Allow == nil {
		return nil
	}
	allow, err := cleanExtensionNames(*fileExtensions.Allow)
	if err != nil {
		return err
	}
	cfg.Extensions.Allow = allow
	cfg.Extensions.Configured = true
	return nil
}

func cleanExtensionNames(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for index, raw := range values {
		name := strings.TrimSpace(raw)
		if name == "" ||
			name == "." ||
			name == ".." ||
			filepath.IsAbs(name) ||
			strings.ContainsAny(name, `/\`) ||
			strings.ContainsRune(name, 0) {
			return nil, fmt.Errorf("extensions.allow[%d] must be a portable extension directory name, got %q", index, raw)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
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

func DefaultToolOutputConfig() ToolOutputConfig {
	return runtimepolicy.DefaultToolOutputPolicy()
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

func validateConfiguredFallbackModels(cfg *Config) error {
	seen := make(map[string]struct{}, len(cfg.FallbackModels))
	normalized := make([]string, 0, len(cfg.FallbackModels))
	for i, raw := range cfg.FallbackModels {
		ref, err := ParseModelRef(raw)
		if err != nil {
			return fmt.Errorf("config: fallback_models[%d]: %w", i, err)
		}
		canonical := ref.String()
		if _, ok := seen[canonical]; ok {
			return fmt.Errorf("config: duplicate fallback_models entry %q", canonical)
		}
		seen[canonical] = struct{}{}
		if err := validateConfiguredModelRef(cfg, ref); err != nil {
			return fmt.Errorf("config: fallback_models[%d]: %w", i, err)
		}
		normalized = append(normalized, canonical)
	}
	cfg.FallbackModels = normalized
	return nil
}

func validateConfiguredModelRef(cfg *Config, ref ModelRef) error {
	provider, ok := cfg.providerConfigs[ref.ProviderID]
	if !ok {
		return fmt.Errorf("model %q references unknown provider %q", ref.String(), ref.ProviderID)
	}
	if _, ok := providerModelByID(provider.Models, ref.ModelID); !ok {
		return fmt.Errorf("model %q references unknown model %q for provider %q", ref.String(), ref.ModelID, ref.ProviderID)
	}
	return nil
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
	if c.MaxAutoFailures > 0 {
		cfg.Compaction.MaxAutoFailures = c.MaxAutoFailures
	}
}

func applyToolOutputConfig(cfg *Config, c toolOutputConfig) {
	if c.InlineMaxBytes > 0 {
		cfg.ToolOutput.InlineMaxBytes = c.InlineMaxBytes
	}
	if c.PreviewHeadBytes > 0 {
		cfg.ToolOutput.PreviewHeadBytes = c.PreviewHeadBytes
	}
	if c.PreviewTailBytes > 0 {
		cfg.ToolOutput.PreviewTailBytes = c.PreviewTailBytes
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
	snapshot := cfg.EnvironmentSnapshot()
	for _, key := range providerEnvKeys {
		if _, skip := excluded[key]; skip {
			continue
		}
		if v, ok := snapshot.Lookup(key); ok && v != "" {
			values[key] = v
		}
	}
	return applyEnvMap(cfg, values)
}

func resolveRuntimeEnvironment(cfg *Config) error {
	dotenvPath := filepath.Join(cfg.WorkDir, ".env")
	layers := make([]environment.Layer, 0, len(cfg.environmentLayers)+1)
	for _, layer := range cfg.environmentLayers {
		if layer.Source == environment.SourceUserConfig {
			layers = append(layers, layer)
		}
	}
	status := EnvironmentStatus{DotenvPath: dotenvPath, DotenvEnabled: cfg.loadDotenv}
	if cfg.loadDotenv {
		result, err := environment.LoadDotenv(dotenvPath, environment.LoadDotenvOptions{})
		if err != nil {
			return err
		}
		status.DotenvLoaded = result.Loaded
		if result.Loaded {
			layers = append(layers, environment.Layer{
				Source: environment.SourceDotenv,
				Path:   result.Path,
				Values: result.Values,
				Strict: true,
			})
		}
	}
	for _, layer := range cfg.environmentLayers {
		if layer.Source != environment.SourceUserConfig {
			layers = append(layers, layer)
		}
	}
	inherited := os.Environ()
	snapshot, err := environment.Resolve(environment.Options{
		Layers:    layers,
		Inherited: inherited,
	})
	if err != nil {
		return err
	}
	status.ConfiguredVariables = len(snapshot.ConfiguredMetadata())
	cfg.runtimeEnvironment = snapshot
	cfg.launchEnvironment = environment.FromEnviron(inherited)
	cfg.runtimeEnvStatus = status
	return nil
}

func cloneEnvironmentVariables(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
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
