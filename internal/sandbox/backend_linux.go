package sandbox

import (
	"os"
	"path/filepath"
)

func prepareLinux(lookPath func(string) (string, error), req Request) (ExecSpec, error) {
	helper, err := lookPath("bwrap")
	if err != nil {
		return ExecSpec{}, NewError(ErrorCodeBackendUnavailable, "linux", "bubblewrap", "lookup", req.Policy, "Install bubblewrap and ensure user namespaces are allowed, set sandbox.enabled: false, or run on a platform with an available sandbox backend.", err)
	}
	if err := ValidateOutsideWorkspaceAccess(req.Policy.FileSystem.OutsideWorkspace); err != nil {
		return ExecSpec{}, err
	}
	roots := normalizedRoots(req.WorkspaceRoots)
	if req.Policy.FileSystem.OutsideWorkspace != OutsideWorkspaceReadWrite && len(roots) == 0 {
		return ExecSpec{}, NewError(ErrorCodePolicyUnavailable, "linux", "bubblewrap", "mount", req.Policy, "A writable workspace root is required when outside_workspace is restricted.", nil)
	}
	args := []string{"--die-with-parent"}
	if !req.Policy.Network.Enabled {
		args = append(args, "--unshare-net")
	}
	switch req.Policy.FileSystem.OutsideWorkspace {
	case OutsideWorkspaceReadWrite:
		args = append(args, "--dev-bind", "/", "/")
	case OutsideWorkspaceReadOnly:
		args = append(args, "--ro-bind", "/", "/")
		args = append(args, "--dev", "/dev", "--tmpfs", "/tmp")
		for _, root := range roots {
			args = append(args, "--bind", root, root)
		}
	case OutsideWorkspaceDenied:
		args = append(args, minimalLinuxReadBinds(req.Policy.Network.Enabled)...)
		args = append(args, "--dev", "/dev", "--tmpfs", "/tmp")
		for _, root := range roots {
			args = append(args, "--bind", root, root)
		}
	}
	if req.Spec.Dir != "" {
		if abs, err := filepath.Abs(req.Spec.Dir); err == nil {
			args = append(args, "--chdir", abs)
		}
	}
	args = append(args, "--")
	args = append(args, req.Spec.Binary)
	args = append(args, req.Spec.Args...)
	wrapped := cloneExecSpec(req.Spec)
	wrapped.Binary = helper
	wrapped.Args = args
	return wrapped, nil
}

func minimalLinuxReadBinds(networkEnabled bool) []string {
	candidates := []string{"/bin", "/usr", "/lib", "/lib64", "/sbin", "/etc/ld.so.cache", "/etc/ld.so.conf"}
	if networkEnabled {
		candidates = append(candidates, "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/protocols", "/etc/services")
	}
	args := []string{}
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		args = append(args, "--ro-bind", path, path)
	}
	if _, err := os.Stat("/proc"); err == nil {
		args = append(args, "--proc", "/proc")
	}
	return args
}
