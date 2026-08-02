package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
)

// AgentRuntimeResolution is the immutable process-lifetime view of selected
// resources, Extension defaults, and Agent-owned Extension data paths.
type AgentRuntimeResolution struct {
	graph                   RuntimeResourceGraph
	environment             environment.Snapshot
	environmentDeclarations []RuntimeExtensionEnvironmentDeclaration
	extensions              AgentExtensionsRuntime
}

type RuntimeExtensionEnvironmentDeclaration struct {
	Name             string
	Source           string
	ManifestPath     string
	Status           environment.DefaultStatus
	ShadowedBySource string
	ShadowedByPath   string
}

func ResolveAgentRuntime(cfg config.Config) (AgentRuntimeResolution, error) {
	return resolveAgentRuntime(cfg, false)
}

// InspectAgentRuntime validates and reports selected Extension defaults without
// requiring an Agent data directory. The returned environment is diagnostic
// only and must not be used to launch runtime children.
func InspectAgentRuntime(cfg config.Config) (AgentRuntimeResolution, error) {
	return resolveAgentRuntime(cfg, true)
}

func resolveAgentRuntime(cfg config.Config, allowMissingAgentData bool) (AgentRuntimeResolution, error) {
	graph, err := ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		return AgentRuntimeResolution{}, err
	}
	resolvedEnvironment, declarations, err := resolveExtensionEnvironment(cfg, graph, allowMissingAgentData)
	resolution := AgentRuntimeResolution{
		graph:                   graph,
		environment:             resolvedEnvironment,
		environmentDeclarations: declarations,
		extensions:              newAgentExtensionsRuntime(cfg.AgentAddress),
	}
	if err != nil {
		return resolution, err
	}
	return resolution, nil
}

func (r AgentRuntimeResolution) ResourceGraph() RuntimeResourceGraph {
	return r.graph
}

func (r AgentRuntimeResolution) Environment() environment.Snapshot {
	return r.environment
}

func (r AgentRuntimeResolution) EnvironmentDeclarations() []RuntimeExtensionEnvironmentDeclaration {
	return append([]RuntimeExtensionEnvironmentDeclaration(nil), r.environmentDeclarations...)
}

func (r AgentRuntimeResolution) ExtensionsRuntime() AgentExtensionsRuntime {
	return r.extensions
}

func resolveExtensionEnvironment(cfg config.Config, graph RuntimeResourceGraph, allowMissingAgentData bool) (environment.Snapshot, []RuntimeExtensionEnvironmentDeclaration, error) {
	workDir := strings.TrimSpace(cfg.WorkDir)
	if workDir != "" {
		if absolute, err := filepath.Abs(workDir); err == nil {
			workDir = absolute
		}
	}
	var defaults []environment.DefaultDeclaration
	for _, descriptor := range graph.Extensions() {
		variables := descriptor.Manifest.Agent.Environment.Variables
		keys := make([]string, 0, len(variables))
		for key := range variables {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			extensionDataDir := descriptor.Runtime.DataDir
			if extensionDataDir == "" && allowMissingAgentData {
				extensionDataDir = "[JUEX_EXT_DATA_DIR:" + descriptor.Name + "]"
			}
			value, err := expandExtensionEnvironmentValue(variables[key], extensionEnvironmentPlaceholders{
				ExtensionDir:     descriptor.Dir,
				ExtensionDataDir: extensionDataDir,
				WorkDir:          workDir,
			})
			if err != nil {
				return environment.Snapshot{}, nil, fmt.Errorf(
					"extensions: environment variable %q from %s: %w",
					key,
					descriptor.Source,
					err,
				)
			}
			defaults = append(defaults, environment.DefaultDeclaration{
				Key: key, Value: value, Source: environment.Source(descriptor.Source),
				Path: filepath.Join(descriptor.Dir, "juex.extension.json"),
			})
		}
	}
	resolved, metadata, err := cfg.EnvironmentSnapshot().WithDefaults(defaults)
	declarations := make([]RuntimeExtensionEnvironmentDeclaration, 0, len(metadata))
	for _, item := range metadata {
		declarations = append(declarations, RuntimeExtensionEnvironmentDeclaration{
			Name: item.Key, Source: string(item.Source), ManifestPath: item.Path, Status: item.Status,
			ShadowedBySource: string(item.ShadowedBy), ShadowedByPath: item.ShadowedPath,
		})
	}
	if err != nil {
		return environment.Snapshot{}, declarations, fmt.Errorf("extensions: resolve Agent environment defaults: %w", err)
	}
	return resolved, declarations, nil
}

type extensionEnvironmentPlaceholders struct {
	ExtensionDir     string
	ExtensionDataDir string
	WorkDir          string
}

func expandExtensionEnvironmentValue(value string, placeholders extensionEnvironmentPlaceholders) (string, error) {
	if strings.ContainsRune(value, '`') {
		return "", fmt.Errorf("backticks are not supported")
	}
	values := map[string]string{
		"JUEX_EXT_DIR":      placeholders.ExtensionDir,
		"JUEX_EXT_DATA_DIR": placeholders.ExtensionDataDir,
		"WORKDIR":           placeholders.WorkDir,
		"JUEX_WORKDIR":      placeholders.WorkDir,
	}
	var out strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '$' {
			out.WriteByte(value[index])
			index++
			continue
		}
		if index+1 >= len(value) {
			out.WriteByte('$')
			index++
			continue
		}
		next := value[index+1]
		switch {
		case next == '{':
			end := strings.IndexByte(value[index+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unclosed placeholder")
			}
			end += index + 2
			name := value[index+2 : end]
			replacement, ok := values[name]
			if !ok {
				return "", fmt.Errorf("placeholder %q is not supported", name)
			}
			if replacement == "" {
				return "", fmt.Errorf("placeholder %q is unresolved", name)
			}
			out.WriteString(replacement)
			index = end + 1
		case next == '(' || next == '_' || next >= 'A' && next <= 'Z' || next >= 'a' && next <= 'z':
			return "", fmt.Errorf("shell-style environment expansion is not supported")
		default:
			out.WriteByte('$')
			index++
		}
	}
	return out.String(), nil
}
