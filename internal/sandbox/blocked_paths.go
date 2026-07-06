package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type PathGuard struct {
	active bool
	base   string
	roots  []blockedPath
}

type blockedPath struct {
	original string
	variants []string
}

func NewPathGuard(workDir string, policy Policy) PathGuard {
	if !policy.Enabled || len(policy.FileSystem.BlockedPaths) == 0 {
		return PathGuard{}
	}
	base := sandboxPathBase(workDir)
	roots := make([]blockedPath, 0, len(policy.FileSystem.BlockedPaths))
	for _, raw := range policy.FileSystem.BlockedPaths {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		variants := normalizedPathVariants(base, trimmed)
		if len(variants) == 0 {
			continue
		}
		roots = append(roots, blockedPath{original: trimmed, variants: variants})
	}
	if len(roots) == 0 {
		return PathGuard{}
	}
	return PathGuard{active: true, base: base, roots: roots}
}

func (g PathGuard) Check(path string) error {
	target, blocked, ok := g.blockedPath(path)
	if !ok {
		return nil
	}
	return fmt.Errorf("sandbox: blocked path %s matches file_system.blocked_paths entry %s", target, blocked)
}

func (g PathGuard) IsBlocked(path string) bool {
	_, _, ok := g.blockedPath(path)
	return ok
}

func (g PathGuard) blockedPath(path string) (string, string, bool) {
	if !g.active || strings.TrimSpace(path) == "" {
		return "", "", false
	}
	for _, target := range normalizedPathVariants(g.base, path) {
		for _, root := range g.roots {
			for _, variant := range root.variants {
				if pathWithinOrEqual(variant, target) {
					return target, root.original, true
				}
			}
		}
	}
	return "", "", false
}

func AppendBlockedPaths(existing []string, incoming []string) ([]string, error) {
	out := make([]string, 0, len(existing)+len(incoming))
	seen := map[string]struct{}{}
	for _, value := range existing {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	for _, value := range incoming {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, fmt.Errorf("sandbox.file_system.blocked_paths entries must not be empty")
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out, nil
}

func normalizedBlockedPaths(base string, paths []string) []string {
	base = sandboxPathBase(base)
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, raw := range paths {
		for _, path := range normalizedPathVariants(base, raw) {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

func sandboxPathBase(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			workDir = cwd
		}
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	return filepath.Clean(workDir)
}

func normalizedPathVariants(base, raw string) []string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return nil
	}
	path = expandHomePath(path)
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	abs = filepath.Clean(abs)
	out := []string{abs}
	if eval, err := evalExistingPathPrefix(abs); err == nil && eval != abs {
		out = append(out, eval)
	}
	return dedupePaths(out)
}

func expandHomePath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func evalExistingPathPrefix(path string) (string, error) {
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(eval), nil
	}
	current := filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			eval, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				eval = filepath.Join(eval, suffix[i])
			}
			return filepath.Clean(eval), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path), nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithinOrEqual(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if caseInsensitivePathMatch() {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func caseInsensitivePathMatch() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

func dedupePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}
