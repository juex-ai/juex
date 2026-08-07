package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspacePath struct {
	Relative string
	Absolute string
}

type workspacePathResolver struct {
	root     string
	evalRoot string
}

func newWorkspacePathResolver(workDir string) (workspacePathResolver, error) {
	root := workDir
	if strings.TrimSpace(root) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return workspacePathResolver{}, err
		}
		root = cwd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return workspacePathResolver{}, err
	}
	absRoot = filepath.Clean(absRoot)
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return workspacePathResolver{}, err
	}
	return workspacePathResolver{root: absRoot, evalRoot: filepath.Clean(evalRoot)}, nil
}

func (r workspacePathResolver) Resolve(input string) (workspacePath, error) {
	if strings.TrimSpace(input) == "" {
		return workspacePath{}, fmt.Errorf("unsafe path %q", input)
	}
	if strings.HasPrefix(input, "//") || strings.HasPrefix(input, `\`) {
		return workspacePath{}, fmt.Errorf("unsafe path %q: UNC, device, and rooted backslash paths are not allowed", input)
	}
	hostPath := filepath.FromSlash(input)
	volume := filepath.VolumeName(hostPath)
	if strings.Contains(hostPath[len(volume):], ":") {
		return workspacePath{}, fmt.Errorf("unsafe path %q: colons outside an absolute volume prefix are not allowed", input)
	}
	if volume != "" && !filepath.IsAbs(hostPath) {
		return workspacePath{}, fmt.Errorf("unsafe path %q: volume-relative paths are not allowed", input)
	}
	if len(hostPath) > 0 && os.IsPathSeparator(hostPath[0]) && !filepath.IsAbs(hostPath) {
		return workspacePath{}, fmt.Errorf("unsafe path %q: rooted paths without an absolute volume are not allowed", input)
	}

	var abs string
	if filepath.IsAbs(hostPath) {
		abs = filepath.Clean(hostPath)
	} else {
		rel := filepath.Clean(hostPath)
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return workspacePath{}, fmt.Errorf("unsafe path %q: path escapes workspace", input)
		}
		abs = filepath.Join(r.root, rel)
	}
	rel, err := filepath.Rel(r.root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return workspacePath{}, fmt.Errorf("unsafe path %q: path escapes workspace", input)
	}
	if err := r.checkSymlinkBoundary(abs, input); err != nil {
		return workspacePath{}, err
	}
	return workspacePath{Relative: filepath.ToSlash(rel), Absolute: abs}, nil
}

func (r workspacePathResolver) checkSymlinkBoundary(abs, input string) error {
	checkPath := abs
	for {
		_, err := os.Lstat(checkPath)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("unsafe path %q: %w", input, err)
		}
		parent := filepath.Dir(checkPath)
		if parent == checkPath {
			return fmt.Errorf("unsafe path %q: path escapes workspace", input)
		}
		checkPath = parent
	}
	evaluated, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		return fmt.Errorf("unsafe path %q: %w", input, err)
	}
	if !pathWithin(r.evalRoot, evaluated) {
		return fmt.Errorf("unsafe path %q: symlink escapes workspace", input)
	}
	return nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
