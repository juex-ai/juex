package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

var linuxMaskSourcesMu sync.Mutex

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
	}
	blockedArgs, err := linuxBlockedPathArgs(normalizedBlockedPaths(firstWorkspaceRoot(roots), req.Policy.FileSystem.BlockedPaths))
	if err != nil {
		return ExecSpec{}, NewError(ErrorCodePolicyUnavailable, "linux", "bubblewrap", "mount", req.Policy, "Unable to prepare blocked_paths mask mounts for the requested sandbox policy.", err)
	}
	args = append(args, blockedArgs...)
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

func linuxBlockedPathArgs(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	emptyDir, emptyFile, err := linuxMaskSources()
	if err != nil {
		return nil, err
	}
	args := []string{}
	for _, path := range paths {
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil && !info.IsDir():
			args = append(args, "--ro-bind", emptyFile, path)
		case statErr == nil:
			args = append(args, "--ro-bind", emptyDir, path)
		case os.IsNotExist(statErr):
			return nil, fmt.Errorf("blocked path %q does not exist; linux bubblewrap cannot mask missing paths without creating host-visible mountpoints", path)
		default:
			return nil, statErr
		}
	}
	return args, nil
}

func linuxMaskSources() (string, string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	root := filepath.Join(base, "juex", "sandbox-mask", strconv.Itoa(os.Getpid()))
	emptyDir := filepath.Join(root, "empty-dir")
	emptyFile := filepath.Join(root, "empty-file")
	linuxMaskSourcesMu.Lock()
	defer linuxMaskSourcesMu.Unlock()
	if linuxMaskSourcesReady(emptyDir, emptyFile) {
		return emptyDir, emptyFile, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(emptyDir, 0o555); err != nil {
		return "", "", err
	}
	_ = os.Chmod(emptyFile, 0o600)
	file, err := os.OpenFile(emptyFile, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", err
	}
	if err := file.Close(); err != nil {
		return "", "", err
	}
	_ = os.Chmod(emptyDir, 0o555)
	_ = os.Chmod(emptyFile, 0o444)
	return emptyDir, emptyFile, nil
}

func linuxMaskSourcesReady(emptyDir, emptyFile string) bool {
	dirInfo, err := os.Stat(emptyDir)
	if err != nil || !dirInfo.IsDir() {
		return false
	}
	fileInfo, err := os.Stat(emptyFile)
	return err == nil && !fileInfo.IsDir() && fileInfo.Size() == 0
}
