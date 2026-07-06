// Package sandbox defines the command execution sandbox contract shared by
// config, tools, and status surfaces.
package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
)

type OutsideWorkspaceAccess string

const (
	OutsideWorkspaceReadWrite OutsideWorkspaceAccess = "read_write"
	OutsideWorkspaceReadOnly  OutsideWorkspaceAccess = "read_only"
	OutsideWorkspaceDenied    OutsideWorkspaceAccess = "denied"
)

type Policy struct {
	Enabled    bool             `json:"enabled"`
	FileSystem FileSystemPolicy `json:"file_system"`
	Network    NetworkPolicy    `json:"network"`
}

type FileSystemPolicy struct {
	OutsideWorkspace OutsideWorkspaceAccess `json:"outside_workspace"`
}

type NetworkPolicy struct {
	Enabled bool `json:"enabled"`
}

func DefaultPolicy() Policy {
	return Policy{
		Enabled: false,
		FileSystem: FileSystemPolicy{
			OutsideWorkspace: OutsideWorkspaceReadWrite,
		},
		Network: NetworkPolicy{
			Enabled: true,
		},
	}
}

func ValidateOutsideWorkspaceAccess(value OutsideWorkspaceAccess) error {
	switch value {
	case OutsideWorkspaceReadWrite, OutsideWorkspaceReadOnly, OutsideWorkspaceDenied:
		return nil
	default:
		return fmt.Errorf("sandbox.file_system.outside_workspace must be one of read_write, read_only, denied, got %q", value)
	}
}

type ExecSpec struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string
}

type Request struct {
	Policy         Policy
	WorkspaceRoots []string
	Spec           ExecSpec
}

type Runner interface {
	Prepare(ctx context.Context, req Request) (ExecSpec, error)
}

type DefaultRunner struct {
	RuntimeOS string
	LookPath  func(string) (string, error)
}

func (r DefaultRunner) Prepare(ctx context.Context, req Request) (ExecSpec, error) {
	_ = ctx
	if !req.Policy.Enabled {
		return cloneExecSpec(req.Spec), nil
	}
	runtimeOS := r.RuntimeOS
	if runtimeOS == "" {
		runtimeOS = runtime.GOOS
	}
	lookPath := r.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	switch runtimeOS {
	case "darwin":
		return prepareDarwin(lookPath, req)
	case "linux":
		return prepareLinux(lookPath, req)
	case "windows":
		return ExecSpec{}, NewError(ErrorCodeUnsupportedPlatform, runtimeOS, "windows", "select", req.Policy, "Windows sandbox execution is not supported yet; set sandbox.enabled: false or run JueX on macOS/Linux.", nil)
	default:
		return ExecSpec{}, NewError(ErrorCodeUnsupportedPlatform, runtimeOS, runtimeOS, "select", req.Policy, "This platform does not have a JueX sandbox backend; set sandbox.enabled: false or run JueX on macOS/Linux.", nil)
	}
}

func cloneExecSpec(spec ExecSpec) ExecSpec {
	return ExecSpec{
		Binary: spec.Binary,
		Args:   append([]string(nil), spec.Args...),
		Dir:    spec.Dir,
		Env:    append([]string(nil), spec.Env...),
	}
}

func requestedPolicyText(policy Policy) string {
	return "file_system.outside_workspace=" + string(policy.FileSystem.OutsideWorkspace) +
		" network.enabled=" + strconv.FormatBool(policy.Network.Enabled)
}
