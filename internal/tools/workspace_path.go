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
	Identity string
}

type workspacePathResolver struct {
	root            string
	evalRoot        string
	caseInsensitive bool
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
	return workspacePathResolver{
		root:            absRoot,
		evalRoot:        filepath.Clean(evalRoot),
		caseInsensitive: workspaceCaseInsensitive(absRoot),
	}, nil
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
	rel, err := r.relative(r.root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return workspacePath{}, fmt.Errorf("unsafe path %q: path escapes workspace", input)
	}
	if err := r.checkSymlinkBoundary(abs, input); err != nil {
		return workspacePath{}, err
	}
	rel = filepath.ToSlash(rel)
	return workspacePath{Relative: rel, Absolute: abs, Identity: r.identity(rel)}, nil
}

func (r workspacePathResolver) identity(rel string) string {
	rel = filepath.ToSlash(rel)
	if r.caseInsensitive {
		return strings.ToLower(rel)
	}
	return rel
}

func (r workspacePathResolver) sameAbsolute(left, right string) bool {
	if r.caseInsensitive {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func (r workspacePathResolver) relative(root, target string) (string, error) {
	comparisonRoot, comparisonTarget := root, target
	if r.caseInsensitive {
		comparisonRoot = strings.ToLower(comparisonRoot)
		comparisonTarget = strings.ToLower(comparisonTarget)
	}
	rel, err := filepath.Rel(comparisonRoot, comparisonTarget)
	if err != nil || !r.caseInsensitive || rel == "." {
		return rel, err
	}
	if len(target) > len(root) && strings.EqualFold(target[:len(root)], root) {
		if strings.HasSuffix(root, string(filepath.Separator)) {
			return target[len(root):], nil
		}
		if os.IsPathSeparator(target[len(root)]) {
			return target[len(root)+1:], nil
		}
	}
	return rel, nil
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
	if !r.pathWithin(r.evalRoot, evaluated) {
		return fmt.Errorf("unsafe path %q: symlink escapes workspace", input)
	}
	return nil
}

func (r workspacePathResolver) pathWithin(root, target string) bool {
	rel, err := r.relative(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
