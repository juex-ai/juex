// Package sandbox defines the command execution sandbox contract shared by
// config, tools, and status surfaces.
package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const (
	sandboxTargetHelperArgument = "__juex_sandbox_target_v1"
	sandboxTargetEnvironmentKey = "JUEX_INTERNAL_SANDBOX_TARGET_ENV_V1"
)

type OutsideWorkspaceAccess string

const (
	OutsideWorkspaceReadWrite OutsideWorkspaceAccess = "read_write"
	OutsideWorkspaceReadOnly  OutsideWorkspaceAccess = "read_only"
)

type Policy struct {
	Enabled    bool             `json:"enabled"`
	FileSystem FileSystemPolicy `json:"file_system"`
	Network    NetworkPolicy    `json:"network"`
}

type FileSystemPolicy struct {
	OutsideWorkspace OutsideWorkspaceAccess `json:"outside_workspace"`
	BlockedPaths     []string               `json:"blocked_paths,omitempty"`
}

type NetworkPolicy struct {
	Enabled bool `json:"enabled"`
}

func DefaultPolicy() Policy {
	return DefaultPolicyForOS(runtime.GOOS)
}

func DisabledPolicy() Policy {
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

func DefaultPolicyForOS(runtimeOS string) Policy {
	policy := DisabledPolicy()
	switch runtimeOS {
	case "darwin", "linux":
		policy.Enabled = true
		policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	}
	return policy
}

func ValidateOutsideWorkspaceAccess(value OutsideWorkspaceAccess) error {
	switch value {
	case OutsideWorkspaceReadWrite, OutsideWorkspaceReadOnly:
		return nil
	default:
		return fmt.Errorf("sandbox.file_system.outside_workspace must be one of read_write, read_only, got %q", value)
	}
}

type ExecSpec struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string
}

type Request struct {
	Policy     Policy
	WorkDir    string
	FilePolicy FilePolicy
	Spec       ExecSpec
}

type Runner interface {
	Prepare(ctx context.Context, req Request) (ExecSpec, error)
}

type DefaultRunner struct {
	RuntimeOS string
	LookPath  func(string) (string, error)
	Probe     ProbeFunc
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
	if err := checkAvailabilityWithProbe(ctx, req.Policy, runtimeOS, lookPath, r.Probe); err != nil {
		return ExecSpec{}, err
	}
	if err := req.FilePolicy.CheckCommandWrites(ctx); err != nil {
		backend, _, _, _ := backendDescriptor(runtimeOS, req.Policy)
		return ExecSpec{}, NewError(
			ErrorCodePolicyUnavailable,
			runtimeOS,
			backend,
			"hard-links",
			req.Policy,
			"Replace multiply linked files in the Workspace or current AgentStateDir with independent files, or explicitly set sandbox.file_system.outside_workspace: read_write.",
			err,
		)
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

// launcherEnvironment removes variables that could inject code into a
// sandbox wrapper before its policy is active, together with the internal
// transport key. Backends restore the deferred entries inside the boundary.
func launcherEnvironment(env []string) []string {
	launcher, _ := partitionSandboxEnvironment(env)
	return launcher
}

func partitionSandboxEnvironment(env []string) ([]string, []string) {
	out := make([]string, 0, len(env))
	deferred := make([]string, 0)
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && (unsafePreSandboxVariable(key) || key == sandboxTargetEnvironmentKey) {
			deferred = append(deferred, item)
			continue
		}
		out = append(out, item)
	}
	return out, deferred
}

func unsafePreSandboxVariable(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	return strings.HasPrefix(upper, "LD_") ||
		strings.HasPrefix(upper, "DYLD_") ||
		upper == "GLIBC_TUNABLES"
}

func sandboxTargetLaunch(spec ExecSpec) (string, []string, []string, error) {
	launcher, deferred := partitionSandboxEnvironment(spec.Env)
	if len(deferred) == 0 {
		return spec.Binary, append([]string(nil), spec.Args...), launcher, nil
	}
	payload, err := json.Marshal(deferred)
	if err != nil {
		return "", nil, nil, fmt.Errorf("sandbox: encode deferred target environment: %w", err)
	}
	helper, err := os.Executable()
	if err != nil {
		return "", nil, nil, fmt.Errorf("sandbox: resolve target helper: %w", err)
	}
	launcher = append(launcher, sandboxTargetEnvironmentKey+"="+base64.RawStdEncoding.EncodeToString(payload))
	args := []string{sandboxTargetHelperArgument, "--", spec.Binary}
	args = append(args, spec.Args...)
	return helper, args, launcher, nil
}

func decodeSandboxTargetEnvironment(value string) ([]string, error) {
	payload, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("sandbox: decode deferred target environment: %w", err)
	}
	var env []string
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("sandbox: parse deferred target environment: %w", err)
	}
	return env, nil
}

func requestedPolicyText(policy Policy) string {
	text := "file_system.outside_workspace=" + string(policy.FileSystem.OutsideWorkspace)
	if len(policy.FileSystem.BlockedPaths) > 0 {
		text += " file_system.blocked_paths=" + strconv.Itoa(len(policy.FileSystem.BlockedPaths))
	}
	return text + " network.enabled=" + strconv.FormatBool(policy.Network.Enabled)
}
