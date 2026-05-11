// Package config wires the runtime: config-file loading, agents-dir resolution,
// and LLM provider construction. Everything that needs a filesystem path lives
// here so other packages can stay path-agnostic.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	ProviderType string // "anthropic" | "openai"
	BaseURL      string
	APIKey       string
	Model        string

	HomeAgentsDir string // ~/.agents (user-global)
	WorkDir       string // explicit; defaults to os.Getwd()
}

type fileConfig struct {
	Provider providerConfig `yaml:"provider"`
}

type providerConfig struct {
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

var providerEnvKeys = []string{"PROVIDER_API_TYPE", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL"}

// Load resolves config from <WorkDir>/.juex/juex.yaml and OS env vars.
//
// Priority (later wins): defaults < <WorkDir>/.juex/juex.yaml < os.Environ.
func Load() (Config, error) {
	return LoadForWorkDir("")
}

// LoadForWorkDir is Load with an explicit working directory.
func LoadForWorkDir(workDir string) (Config, error) {
	cfg := Config{}

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
	return cfg, nil
}

// LoadFromFile is a convenience for tests / `juex run --config <path>`.
// It applies overrides from path on top of Load(); WorkDir is unaffected.
// Legacy .env-style files are accepted only when explicitly passed.
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
		cfg, err = LoadForWorkDir(workDir)
	} else {
		cfg, err = Load()
	}
	if err != nil {
		return cfg, err
	}
	if isDotEnvPath(path) {
		cfg, err = loadFromDotEnvFile(cfg, path)
	} else {
		err = applyYAMLFile(&cfg, path, false)
	}
	if err != nil {
		return cfg, err
	}
	applyOSEnv(&cfg)
	return cfg, nil
}

// NewProvider constructs the LLM provider implied by the config.
func (c Config) NewProvider() (llm.Provider, error) {
	if c.ProviderType == "" {
		return nil, fmt.Errorf("config: PROVIDER_API_TYPE is empty")
	}
	return llm.New(llm.Config{
		Type:    c.ProviderType,
		BaseURL: c.BaseURL,
		APIKey:  c.APIKey,
		Model:   c.Model,
	})
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

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out, sc.Err()
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
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	applyProviderConfig(cfg, fc.Provider)
	return nil
}

func applyProviderConfig(cfg *Config, p providerConfig) {
	if p.Type != "" {
		cfg.ProviderType = p.Type
	}
	if p.BaseURL != "" {
		cfg.BaseURL = p.BaseURL
	}
	if p.APIKey != "" {
		cfg.APIKey = p.APIKey
	}
	if p.Model != "" {
		cfg.Model = p.Model
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

func loadFromDotEnvFile(cfg Config, path string) (Config, error) {
	overrides, err := readEnvFile(path)
	if err != nil {
		return cfg, err
	}
	applyEnvMap(&cfg, overrides)
	return cfg, nil
}

func applyEnvMap(cfg *Config, values map[string]string) {
	if v, ok := values["PROVIDER_API_TYPE"]; ok && v != "" {
		cfg.ProviderType = v
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
}

func isDotEnvPath(path string) bool {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	if ext == ".yaml" || ext == ".yml" {
		return false
	}
	return strings.HasPrefix(base, ".env")
}
