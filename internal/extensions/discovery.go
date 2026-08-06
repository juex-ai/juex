// Package extensions discovers Juex Extensions and reports their standard
// resources.
package extensions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/environment"
)

const (
	SourcePrefix     = "ext:"
	EnvDirKey        = "JUEX_EXT_DIR"
	manifestFilename = "juex.extension.json"
)

var semVerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type Scope string

const (
	ScopeDefaultHome  Scope = "default_home"
	ScopeInstanceHome Scope = "instance_home"
	ScopeProject      Scope = "project"
)

type Manifest struct {
	ManifestVersion int                   `json:"manifest_version"`
	Name            string                `json:"name"`
	Version         string                `json:"version"`
	Description     string                `json:"description,omitempty"`
	DisplayName     string                `json:"display_name,omitempty"`
	Author          string                `json:"author,omitempty"`
	Homepage        string                `json:"homepage,omitempty"`
	Repository      string                `json:"repository,omitempty"`
	License         string                `json:"license,omitempty"`
	Requirements    []ManifestRequirement `json:"requirements,omitempty"`
	Agent           ManifestAgent         `json:"agent,omitempty"`
}

type ManifestRequirement struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

type ManifestAgent struct {
	Environment ManifestEnvironment `json:"environment,omitempty"`
}

type ManifestEnvironment struct {
	Variables map[string]string `json:"variables,omitempty"`
}

type DiscoverOptions struct {
	Roots        []Root
	AllowedNames []string
}

// Root is one installed extension directory in low-to-high precedence order.
type Root struct {
	Path         string
	Scope        Scope
	RequireTrust bool
}

type Extension struct {
	Name         string
	Dir          string
	Scope        Scope
	Source       string
	RequireTrust bool
	Manifest     Manifest
}

type ResourceRef struct {
	Path          string
	Source        string
	ExtensionName string
	ExtensionDir  string
	RequireTrust  bool
}

type Resources struct {
	Extensions        []Extension
	SkillDirs         []ResourceRef
	MCPConfigs        []ResourceRef
	HookFiles         []ResourceRef
	ObservableConfigs []ResourceRef
}

func Discover(opts DiscoverOptions) (Resources, error) {
	var roots []extensionRoot
	for _, root := range opts.Roots {
		if root.Path == "" {
			continue
		}
		roots = appendDistinctExtensionRoot(roots, extensionRoot(root))
	}
	if len(opts.AllowedNames) == 0 {
		return Resources{}, nil
	}

	allowed := make(map[string]struct{}, len(opts.AllowedNames))
	for _, name := range opts.AllowedNames {
		allowed[name] = struct{}{}
	}
	type selectedExtension struct {
		Extension
		RequireTrust bool
	}
	selected := make(map[string]selectedExtension)
	for _, root := range roots {
		discovered, err := discoverRoot(root)
		if err != nil {
			return Resources{}, err
		}
		for _, ext := range discovered {
			if _, ok := allowed[ext.Name]; !ok {
				continue
			}
			selected[ext.Name] = selectedExtension{Extension: ext, RequireTrust: root.RequireTrust}
		}
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	var out Resources
	for _, name := range names {
		selection := selected[name]
		ext := selection.Extension
		ext.RequireTrust = selection.RequireTrust
		manifest, err := loadManifest(ext)
		if err != nil {
			return Resources{}, err
		}
		ext.Manifest = manifest
		out.Extensions = append(out.Extensions, ext)
		ref := ResourceRef{
			Source:        ext.Source,
			ExtensionName: ext.Name,
			ExtensionDir:  ext.Dir,
			RequireTrust:  selection.RequireTrust,
		}
		if ok, err := skillDirExists(filepath.Join(ext.Dir, "skills")); err != nil {
			return Resources{}, err
		} else if ok {
			skillRef := ref
			skillRef.Path = filepath.Join(ext.Dir, "skills")
			out.SkillDirs = append(out.SkillDirs, skillRef)
		}
		if ok, err := pathExists(filepath.Join(ext.Dir, "mcp.json")); err != nil {
			return Resources{}, err
		} else if ok {
			mcpRef := ref
			mcpRef.Path = filepath.Join(ext.Dir, "mcp.json")
			out.MCPConfigs = append(out.MCPConfigs, mcpRef)
		}
		if ok, err := pathExists(filepath.Join(ext.Dir, "hooks.yaml")); err != nil {
			return Resources{}, err
		} else if ok {
			hookRef := ref
			hookRef.Path = filepath.Join(ext.Dir, "hooks.yaml")
			out.HookFiles = append(out.HookFiles, hookRef)
		}
		if ok, err := regularFileExists(filepath.Join(ext.Dir, "observables.json")); err != nil {
			return Resources{}, err
		} else if ok {
			observableRef := ref
			observableRef.Path = filepath.Join(ext.Dir, "observables.json")
			out.ObservableConfigs = append(out.ObservableConfigs, observableRef)
		}
	}
	return out, nil
}

func loadManifest(ext Extension) (Manifest, error) {
	path := filepath.Join(ext.Dir, manifestFilename)
	entries, err := os.ReadDir(ext.Dir)
	if err != nil {
		return Manifest{}, manifestError(ext.Name, path, "read", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == manifestFilename {
			found = true
			break
		}
	}
	if !found {
		return Manifest{}, manifestError(ext.Name, path, "read", os.ErrNotExist)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, manifestError(ext.Name, path, "read", err)
	}
	if err := validateUniqueJSONKeys(data); err != nil {
		return Manifest{}, manifestError(ext.Name, path, "parse", err)
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&fields); err != nil {
		return Manifest{}, manifestError(ext.Name, path, "parse", err)
	}
	if fields == nil {
		return Manifest{}, manifestError(ext.Name, path, "parse", fmt.Errorf("manifest must be a JSON object"))
	}
	manifest, err := manifestFromFields(fields)
	if err != nil {
		return Manifest{}, manifestError(ext.Name, path, "validate", err)
	}
	if manifest.ManifestVersion != 1 {
		return Manifest{}, manifestError(ext.Name, path, "validate", fmt.Errorf("manifest_version must be 1, got %d", manifest.ManifestVersion))
	}
	if manifest.Name != ext.Name {
		return Manifest{}, manifestError(ext.Name, path, "validate", fmt.Errorf("name %q must match containing directory %q", manifest.Name, ext.Name))
	}
	if !semVerPattern.MatchString(manifest.Version) {
		return Manifest{}, manifestError(ext.Name, path, "validate", fmt.Errorf("version %q must be valid SemVer", manifest.Version))
	}
	return manifest, nil
}

func manifestFromFields(fields map[string]json.RawMessage) (Manifest, error) {
	manifestVersion, err := requiredManifestInt(fields, "manifest_version")
	if err != nil {
		return Manifest{}, err
	}
	name, err := requiredManifestString(fields, "name")
	if err != nil {
		return Manifest{}, err
	}
	version, err := requiredManifestString(fields, "version")
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{ManifestVersion: manifestVersion, Name: name, Version: version}
	optional := []struct {
		name string
		dst  *string
	}{
		{name: "description", dst: &manifest.Description},
		{name: "display_name", dst: &manifest.DisplayName},
		{name: "author", dst: &manifest.Author},
		{name: "homepage", dst: &manifest.Homepage},
		{name: "repository", dst: &manifest.Repository},
		{name: "license", dst: &manifest.License},
	}
	for _, field := range optional {
		raw, ok := fields[field.name]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, field.dst); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if err == nil {
				err = fmt.Errorf("must be a string")
			}
			return Manifest{}, fmt.Errorf("%s %w", field.name, err)
		}
	}
	if raw, ok := fields["agent"]; ok {
		agent, err := manifestAgentFromRaw(raw)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Agent = agent
	}
	if raw, ok := fields["requirements"]; ok {
		requirements, err := manifestRequirementsFromRaw(raw)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Requirements = requirements
	}
	return manifest, nil
}

func manifestAgentFromRaw(raw json.RawMessage) (ManifestAgent, error) {
	fields, err := objectFields(raw, "agent")
	if err != nil {
		return ManifestAgent{}, err
	}
	var agent ManifestAgent
	if environmentRaw, ok := fields["environment"]; ok {
		environmentManifest, err := manifestEnvironmentFromRaw(environmentRaw)
		if err != nil {
			return ManifestAgent{}, err
		}
		agent.Environment = environmentManifest
	}
	return agent, nil
}

func manifestEnvironmentFromRaw(raw json.RawMessage) (ManifestEnvironment, error) {
	fields, err := objectFields(raw, "agent.environment")
	if err != nil {
		return ManifestEnvironment{}, err
	}
	var manifest ManifestEnvironment
	variablesRaw, ok := fields["variables"]
	if !ok {
		return manifest, nil
	}
	if bytes.Equal(bytes.TrimSpace(variablesRaw), []byte("null")) {
		return ManifestEnvironment{}, fmt.Errorf("agent.environment.variables must be an object")
	}
	var variables map[string]string
	if err := json.Unmarshal(variablesRaw, &variables); err != nil {
		return ManifestEnvironment{}, fmt.Errorf("agent.environment.variables values must be strings: %w", err)
	}
	if variables == nil {
		return ManifestEnvironment{}, fmt.Errorf("agent.environment.variables must be an object")
	}
	for _, name := range sortedStringMapKeys(variables) {
		if err := environment.ValidateExtensionDefault(name, variables[name]); err != nil {
			return ManifestEnvironment{}, fmt.Errorf("agent.environment.variables: %w", err)
		}
	}
	manifest.Variables = variables
	return manifest, nil
}

func manifestRequirementsFromRaw(raw json.RawMessage) ([]ManifestRequirement, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("requirements must be an array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, fmt.Errorf("requirements must be an array")
	}
	requirements := make([]ManifestRequirement, 0, len(items))
	for index, item := range items {
		prefix := fmt.Sprintf("requirements[%d]", index)
		fields, err := objectFields(item, prefix)
		if err != nil {
			return nil, err
		}
		name, err := requiredNonEmptyManifestString(fields, "name", prefix+".name")
		if err != nil {
			return nil, err
		}
		description, err := requiredNonEmptyManifestString(fields, "description", prefix+".description")
		if err != nil {
			return nil, err
		}
		requirementURL, err := requiredNonEmptyManifestString(fields, "url", prefix+".url")
		if err != nil {
			return nil, err
		}
		requirements = append(requirements, ManifestRequirement{
			Name:        name,
			Description: description,
			URL:         requirementURL,
		})
	}
	return requirements, nil
}

func objectFields(raw json.RawMessage, name string) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", name, err)
	}
	if fields == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return fields, nil
}

func requiredNonEmptyManifestString(fields map[string]json.RawMessage, fieldName, displayName string) (string, error) {
	raw, ok := fields[fieldName]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("%s is required", displayName)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", displayName, err)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be empty", displayName)
	}
	return value, nil
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func requiredManifestInt(fields map[string]json.RawMessage, name string) (int, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("%s is required", name)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return value, nil
}

func requiredManifestString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("%s is required", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", name, err)
	}
	return value, nil
}

func validateUniqueJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func manifestError(name, path, action string, err error) error {
	return fmt.Errorf("extensions: extension %q manifest %s: %s: %w", name, path, action, err)
}

func Source(name string) string {
	return SourcePrefix + name
}

func IsExtensionSource(source string) bool {
	return strings.HasPrefix(source, SourcePrefix)
}

type extensionRoot struct {
	Path         string
	Scope        Scope
	RequireTrust bool
}

func appendDistinctExtensionRoot(roots []extensionRoot, candidate extensionRoot) []extensionRoot {
	for index, root := range roots {
		if sameExtensionRoot(root.Path, candidate.Path) {
			copy(roots[index:], roots[index+1:])
			roots = roots[:len(roots)-1]
			return append(roots, candidate)
		}
	}
	return append(roots, candidate)
}

func sameExtensionRoot(left, right string) bool {
	left = absoluteCleanPath(left)
	right = absoluteCleanPath(right)
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func absoluteCleanPath(path string) string {
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return path
}

func discoverRoot(root extensionRoot) ([]Extension, error) {
	entries, err := os.ReadDir(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Extension
	for _, entry := range entries {
		dir, ok := extensionDirPath(root.Path, entry)
		if !ok {
			continue
		}
		name := entry.Name()
		if name == "" {
			continue
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		out = append(out, Extension{
			Name:   name,
			Dir:    dir,
			Scope:  root.Scope,
			Source: Source(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func extensionDirPath(root string, entry os.DirEntry) (string, bool) {
	path := filepath.Join(root, entry.Name())
	if entry.IsDir() {
		return path, true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return path, true
}

func skillDirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("extensions: stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("extensions: %s is not a directory", path)
	}
	return true, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("extensions: stat %s: %w", path, err)
	}
	return true, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("extensions: stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("extensions: %s is not a regular file", path)
	}
	return true, nil
}
