// Package config wires the runtime: env loading, agents-dir resolution, and
// LLM provider construction. Everything that needs a filesystem path lives
// here so other packages can stay path-agnostic.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

// Config holds runtime-wide settings.
//
// HomeAgentsDir hosts user-global resources (AGENTS.md, skills, mcp.json).
// WorkDir hosts work-local resources (project AGENTS.md, project skills,
// project mcp.json, memory entries, session jsonl). Sessions and memory are
// **not** read from HomeAgentsDir in v0.0.1.
type Config struct {
	ProviderType string // "anthropic" | "openai"
	BaseURL      string
	APIKey       string
	Model        string

	HomeAgentsDir string // ~/.agents (user-global)
	WorkDir       string // explicit; defaults to os.Getwd()
}

// Load resolves config from env vars and optional .env files.
//
// Priority (later wins): defaults < ~/.agents/.env < <WorkDir>/.env < os.Environ.
func Load() (Config, error) {
	cfg := Config{}

	cwd, err := os.Getwd()
	if err != nil {
		return cfg, err
	}
	cfg.WorkDir = cwd
	if home, err := os.UserHomeDir(); err == nil {
		cfg.HomeAgentsDir = filepath.Join(home, ".agents")
	}

	envFiles := []string{
		filepath.Join(cfg.HomeAgentsDir, ".env"),
		filepath.Join(cwd, ".env"),
	}
	envSnapshot := map[string]string{}
	for _, p := range envFiles {
		if data, err := readEnvFile(p); err == nil {
			for k, v := range data {
				envSnapshot[k] = v
			}
		}
	}
	for _, key := range []string{"PROVIDER_API_TYPE", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL"} {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			envSnapshot[key] = v
		}
	}
	cfg.ProviderType = envSnapshot["PROVIDER_API_TYPE"]
	cfg.BaseURL = envSnapshot["PROVIDER_API_BASE"]
	cfg.APIKey = envSnapshot["PROVIDER_API_KEY"]
	cfg.Model = envSnapshot["PROVIDER_API_MODEL"]
	return cfg, nil
}

// LoadFromFile is a convenience for tests / `juex run --env <path>`.
// It applies overrides from path on top of Load(); WorkDir is unaffected.
func LoadFromFile(path string) (Config, error) {
	cfg, err := Load()
	if err != nil {
		return cfg, err
	}
	overrides, err := readEnvFile(path)
	if err != nil {
		return cfg, err
	}
	if v, ok := overrides["PROVIDER_API_TYPE"]; ok && v != "" {
		cfg.ProviderType = v
	}
	if v, ok := overrides["PROVIDER_API_BASE"]; ok && v != "" {
		cfg.BaseURL = v
	}
	if v, ok := overrides["PROVIDER_API_KEY"]; ok && v != "" {
		cfg.APIKey = v
	}
	if v, ok := overrides["PROVIDER_API_MODEL"]; ok && v != "" {
		cfg.Model = v
	}
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
	return filepath.Join(c.WorkDir, ".agents", "memory")
}

// SessionsDir returns the work-local sessions root.
func (c Config) SessionsDir() string {
	if c.WorkDir == "" {
		return ""
	}
	return filepath.Join(c.WorkDir, ".agents", "sessions")
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
