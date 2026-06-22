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
	HomeJuexDir               string
	WorkDir                   string
	EnableUserGlobalResources bool
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
	Extensions []Extension
	SkillDirs  []ResourceRef
	MCPConfigs []ResourceRef
	HookFiles  []ResourceRef
}

func Discover(opts DiscoverOptions) (Resources, error) {
	var roots []extensionRoot
	if opts.EnableUserGlobalResources && opts.HomeJuexDir != "" {
		roots = append(roots, extensionRoot{
			Path:         filepath.Join(opts.HomeJuexDir, "extensions"),
			Scope:        ScopeUser,
			RequireTrust: false,
		})
	}
	if opts.WorkDir != "" {
		roots = append(roots, extensionRoot{
			Path:         filepath.Join(opts.WorkDir, ".juex", "extensions"),
			Scope:        ScopeProject,
			RequireTrust: true,
		})
	}

	var out Resources
	seen := map[string]Extension{}
	for _, root := range roots {
		extensions, err := discoverRoot(root)
		if err != nil {
			return Resources{}, err
		}
		for _, ext := range extensions {
			if prev, ok := seen[ext.Name]; ok {
				return Resources{}, fmt.Errorf("extensions: duplicate extension %q in %s and %s", ext.Name, prev.Dir, ext.Dir)
			}
			seen[ext.Name] = ext
			out.Extensions = append(out.Extensions, ext)
			ref := ResourceRef{
				Source:        ext.Source,
				ExtensionName: ext.Name,
				ExtensionDir:  ext.Dir,
				RequireTrust:  root.RequireTrust,
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
