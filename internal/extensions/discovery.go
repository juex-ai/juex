// Package extensions discovers JueX extension bundles and reports the
// standard resources they contribute.
package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SourcePrefix = "ext:"
	EnvDirKey    = "JUEX_EXT_DIR"
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

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
	Name   string
	Dir    string
	Scope  Scope
	Source string
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
