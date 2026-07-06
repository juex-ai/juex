package sandbox

import (
	"fmt"
	"strconv"
	"strings"
)

func prepareDarwin(lookPath func(string) (string, error), req Request) (ExecSpec, error) {
	helper, err := lookPath("sandbox-exec")
	if err != nil {
		return ExecSpec{}, NewError(ErrorCodeBackendUnavailable, "darwin", "sandbox-exec", "lookup", req.Policy, "Install or enable sandbox-exec, set sandbox.enabled: false, or choose a platform backend that can enforce the requested policy.", err)
	}
	profile, err := darwinProfile(req.Policy, req.WorkspaceRoots)
	if err != nil {
		return ExecSpec{}, err
	}
	wrapped := cloneExecSpec(req.Spec)
	original := append([]string{req.Spec.Binary}, req.Spec.Args...)
	wrapped.Binary = helper
	wrapped.Args = append([]string{"-p", profile}, original...)
	return wrapped, nil
}

func darwinProfile(policy Policy, workspaceRoots []string) (string, error) {
	if policy.FileSystem.OutsideWorkspace == OutsideWorkspaceDenied {
		return "", NewError(ErrorCodePolicyUnavailable, "darwin", "sandbox-exec", "profile", policy, "The current macOS backend cannot safely enforce denied outside-workspace reads for arbitrary shells; use read_only, set sandbox.enabled: false, or run with a stronger backend.", nil)
	}
	if err := ValidateOutsideWorkspaceAccess(policy.FileSystem.OutsideWorkspace); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	if policy.FileSystem.OutsideWorkspace == OutsideWorkspaceReadOnly {
		roots := normalizedRoots(workspaceRoots)
		if len(roots) == 0 {
			return "", NewError(ErrorCodePolicyUnavailable, "darwin", "sandbox-exec", "profile", policy, "A writable workspace root is required when outside_workspace is read_only.", nil)
		}
		fmt.Fprintf(&b, "(deny file-write* (require-not %s))\n", darwinWritablePathPredicate(roots))
	}
	if !policy.Network.Enabled {
		b.WriteString("(deny network*)\n")
	}
	return b.String(), nil
}

func darwinWritablePathPredicate(workspaceRoots []string) string {
	parts := make([]string, 0, len(workspaceRoots)+7)
	parts = append(parts, "require-any")
	for _, path := range workspaceRoots {
		parts = append(parts, "(subpath "+strconv.Quote(path)+")")
	}
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		parts = append(parts, "(literal "+strconv.Quote(path)+")")
	}
	for _, path := range []string{"/private/tmp", "/tmp", "/private/var/folders", "/var/folders"} {
		parts = append(parts, "(subpath "+strconv.Quote(path)+")")
	}
	return "(" + strings.Join(parts, " ") + ")"
}
