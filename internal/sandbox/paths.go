package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

func prepareWritableRoots(req Request) (Request, error) {
	workspaceBase := firstWorkspaceRoot(req.WorkspaceRoots)
	for _, additional := range req.AdditionalWritableRoots {
		additionalVariants := normalizedPathVariants(workspaceBase, additional)
		for _, blocked := range req.Policy.FileSystem.BlockedPaths {
			blockedVariants := normalizedPathVariants(workspaceBase, blocked)
			for _, writablePath := range additionalVariants {
				for _, blockedPath := range blockedVariants {
					if pathWithinOrEqual(writablePath, blockedPath) || pathWithinOrEqual(blockedPath, writablePath) {
						return Request{}, fmt.Errorf(
							"additional writable root %q overlaps sandbox.file_system.blocked_paths entry %q",
							additional,
							blocked,
						)
					}
				}
			}
		}
	}
	combined := append([]string(nil), req.WorkspaceRoots...)
	combined = append(combined, req.AdditionalWritableRoots...)
	req.WorkspaceRoots = normalizedRoots(combined)
	req.AdditionalWritableRoots = nil
	return req, nil
}

func normalizedRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err == nil {
			root = abs
		}
		if eval, err := filepath.EvalSymlinks(root); err == nil {
			root = eval
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

func firstWorkspaceRoot(roots []string) string {
	normalized := normalizedRoots(roots)
	if len(normalized) == 0 {
		return ""
	}
	return normalized[0]
}
