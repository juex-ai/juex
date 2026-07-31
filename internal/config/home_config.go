package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/environment"
)

type configScope uint8

const (
	configScopeDefaultHome configScope = iota
	configScopeInstanceHome
	configScopeWorkspace
	configScopeExplicit
)

type yamlConfigSource struct {
	Path      string
	Scope     configScope
	MissingOK bool
}

func (s yamlConfigSource) hookSource() string {
	switch s.Scope {
	case configScopeDefaultHome:
		return "home:default"
	case configScopeInstanceHome:
		return "home:instance"
	default:
		return "project"
	}
}

func (s yamlConfigSource) requireHookTrust() bool {
	return s.Scope == configScopeWorkspace || s.Scope == configScopeExplicit
}

func (s yamlConfigSource) environmentSource() environment.Source {
	switch s.Scope {
	case configScopeWorkspace:
		return environment.SourceWorkspaceConfig
	case configScopeExplicit:
		return environment.SourceExplicitConfig
	default:
		return environment.SourceUserConfig
	}
}

func (s yamlConfigSource) allowsFleet() bool {
	return s.Scope == configScopeDefaultHome || s.Scope == configScopeInstanceHome
}

type homeConfigResolution struct {
	DefaultConfigPath string
	EffectiveHomeDir  string
	Sources           []yamlConfigSource
}

func resolveHomeConfigSources() (homeConfigResolution, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return homeConfigResolution{}, fmt.Errorf("config: resolve user home: %w", err)
	}
	defaultHome, err := canonicalHomeConfigDir(filepath.Join(userHome, ".juex"))
	if err != nil {
		return homeConfigResolution{}, fmt.Errorf("config: resolve default JueX home: %w", err)
	}
	effectiveHome, err := agentstate.EffectiveHome()
	if err != nil {
		return homeConfigResolution{}, err
	}
	resolution := homeConfigResolution{
		DefaultConfigPath: filepath.Join(defaultHome, "juex.yaml"),
		EffectiveHomeDir:  effectiveHome,
		Sources: []yamlConfigSource{{
			Path:      filepath.Join(defaultHome, "juex.yaml"),
			Scope:     configScopeDefaultHome,
			MissingOK: true,
		}},
	}
	if effectiveHome != defaultHome {
		resolution.Sources = append(resolution.Sources, yamlConfigSource{
			Path:      filepath.Join(effectiveHome, "juex.yaml"),
			Scope:     configScopeInstanceHome,
			MissingOK: true,
		})
	}
	return resolution, nil
}

func canonicalHomeConfigDir(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return abs, nil
}

func workspaceYAMLSource(path string) yamlConfigSource {
	return yamlConfigSource{Path: path, Scope: configScopeWorkspace, MissingOK: true}
}

func explicitYAMLSource(path string) yamlConfigSource {
	return yamlConfigSource{Path: path, Scope: configScopeExplicit}
}
