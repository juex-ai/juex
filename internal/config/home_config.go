package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

func (s yamlConfigSource) allowsExtensionPolicy() bool {
	return s.Scope != configScopeExplicit
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
	sameHome, err := sameConfigPath(defaultHome, effectiveHome)
	if err != nil {
		return homeConfigResolution{}, fmt.Errorf("config: compare default and effective JueX homes: %w", err)
	}
	if !sameHome {
		resolution.Sources = append(resolution.Sources, yamlConfigSource{
			Path:      filepath.Join(effectiveHome, "juex.yaml"),
			Scope:     configScopeInstanceHome,
			MissingOK: true,
		})
	}
	return resolution, nil
}

func sameConfigPath(left, right string) (bool, error) {
	if left == right {
		return true, nil
	}
	leftInfo, err := os.Stat(left)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func sameConfigPathSpelling(left, right string) (bool, error) {
	leftParent, leftBase, err := canonicalConfigPathParts(left)
	if err != nil {
		return false, err
	}
	rightParent, rightBase, err := canonicalConfigPathParts(right)
	if err != nil {
		return false, err
	}
	sameParent, err := sameConfigPath(leftParent, rightParent)
	if err != nil || !sameParent {
		return false, err
	}
	if leftBase == rightBase {
		return true, nil
	}
	if !strings.EqualFold(leftBase, rightBase) {
		return false, nil
	}
	leftName, err := filesystemEntryName(leftParent, leftBase)
	if err != nil {
		return false, err
	}
	rightName, err := filesystemEntryName(rightParent, rightBase)
	if err != nil {
		return false, err
	}
	return leftName == rightName, nil
}

func canonicalConfigPathParts(path string) (string, string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	dir, err := canonicalHomeConfigDir(filepath.Dir(abs))
	if err != nil {
		return "", "", err
	}
	return dir, filepath.Base(abs), nil
}

func filesystemEntryName(parent, base string) (string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return base, nil
		}
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() == base {
			return base, nil
		}
	}
	pathInfo, err := os.Stat(filepath.Join(parent, base))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return base, nil
		}
		return "", err
	}
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), base) {
			continue
		}
		entryInfo, err := os.Stat(filepath.Join(parent, entry.Name()))
		if err != nil {
			return "", err
		}
		if os.SameFile(pathInfo, entryInfo) {
			return entry.Name(), nil
		}
	}
	return base, nil
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
