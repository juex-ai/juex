package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const backendProbeTimeout = 3 * time.Second

type ProbeFunc func(context.Context, string, string, Policy) error

type Capability struct {
	Enabled    bool
	Platform   string
	Backend    string
	Available  bool
	ErrorCode  ErrorCode
	Error      string
	Suggestion string
}

type probeResult struct {
	once sync.Once
	err  error
}

var (
	backendProbeCache sync.Map
	runProbeCommand   = executeProbeCommand
)

func CheckAvailability(ctx context.Context, policy Policy, runtimeOS string, lookPath func(string) (string, error)) error {
	return checkAvailabilityWithProbe(ctx, policy, runtimeOS, lookPath, nil)
}

func InspectCapability(ctx context.Context, policy Policy, runtimeOS string, lookPath func(string) (string, error)) Capability {
	capability := Capability{Enabled: policy.Enabled, Platform: runtimeOS}
	backend, _, suggestion, _ := backendDescriptor(runtimeOS, policy)
	capability.Backend = backend
	capability.Suggestion = suggestion
	if !policy.Enabled {
		return capability
	}
	err := CheckAvailability(ctx, policy, runtimeOS, lookPath)
	if err == nil {
		capability.Available = true
		capability.Suggestion = ""
		return capability
	}
	capability.Error = err.Error()
	var sandboxErr *Error
	if errors.As(err, &sandboxErr) {
		capability.ErrorCode = sandboxErr.Code
		capability.Backend = sandboxErr.Backend
		capability.Suggestion = sandboxErr.Suggestion
	}
	return capability
}

func checkAvailabilityWithProbe(ctx context.Context, policy Policy, runtimeOS string, lookPath func(string) (string, error), probe ProbeFunc) error {
	if !policy.Enabled {
		return nil
	}
	backend, executable, suggestion, err := backendDescriptor(runtimeOS, policy)
	if err != nil {
		return err
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	helper, err := lookPath(executable)
	if err != nil {
		return NewError(ErrorCodeBackendUnavailable, runtimeOS, backend, "lookup", policy, suggestion, err)
	}
	if probe == nil {
		target, targetErr := lookPath("true")
		if targetErr != nil {
			return NewError(ErrorCodeBackendUnavailable, runtimeOS, backend, "probe", policy, suggestion, fmt.Errorf("resolve probe target true: %w", targetErr))
		}
		if err := cachedFunctionalProbe(ctx, runtimeOS, helper, target, policy); err != nil {
			return NewError(ErrorCodeBackendUnavailable, runtimeOS, backend, "probe", policy, suggestion, err)
		}
		return nil
	}
	if err := probe(ctx, runtimeOS, helper, policy); err != nil {
		return NewError(ErrorCodeBackendUnavailable, runtimeOS, backend, "probe", policy, suggestion, err)
	}
	return nil
}

func backendDescriptor(runtimeOS string, policy Policy) (backend, executable, suggestion string, err error) {
	switch runtimeOS {
	case "darwin":
		return "sandbox-exec", "sandbox-exec", "Install or enable sandbox-exec, set sandbox.enabled: false, or run JueX on Linux with bubblewrap.", nil
	case "linux":
		return "bubblewrap", "bwrap", "Install bubblewrap and ensure user namespaces and the requested network namespace are allowed, or explicitly set sandbox.enabled: false.", nil
	case "windows":
		return "windows", "", "Windows sandbox execution is not supported yet; set sandbox.enabled: false or run JueX on macOS/Linux.", NewError(ErrorCodeUnsupportedPlatform, runtimeOS, "windows", "select", policy, "Windows sandbox execution is not supported yet; set sandbox.enabled: false or run JueX on macOS/Linux.", nil)
	default:
		return runtimeOS, "", "This platform does not have a JueX sandbox backend; set sandbox.enabled: false or run JueX on macOS/Linux.", NewError(ErrorCodeUnsupportedPlatform, runtimeOS, runtimeOS, "select", policy, "This platform does not have a JueX sandbox backend; set sandbox.enabled: false or run JueX on macOS/Linux.", nil)
	}
}

func cachedFunctionalProbe(ctx context.Context, runtimeOS, helper, target string, policy Policy) error {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00network=%t", runtimeOS, helper, target, policy.Network.Enabled)
	value, _ := backendProbeCache.LoadOrStore(key, &probeResult{})
	result := value.(*probeResult)
	result.once.Do(func() {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendProbeTimeout)
		defer cancel()
		result.err = functionalProbe(probeCtx, runtimeOS, helper, target, policy)
	})
	return result.err
}

func functionalProbe(ctx context.Context, runtimeOS, helper, target string, policy Policy) error {
	var args []string
	switch runtimeOS {
	case "linux":
		args = []string{"--die-with-parent"}
		if !policy.Network.Enabled {
			args = append(args, "--unshare-net")
		}
		args = append(args, "--ro-bind", "/", "/", "--dev", "/dev", "--", target)
	case "darwin":
		args = []string{"-p", "(version 1)\n(allow default)\n", target}
	default:
		return fmt.Errorf("unsupported probe platform %q", runtimeOS)
	}
	return runProbeCommand(ctx, helper, args...)
}

func executeProbeCommand(ctx context.Context, helper string, args ...string) error {
	out, err := exec.CommandContext(ctx, helper, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func resetBackendProbeCacheForTest() {
	backendProbeCache = sync.Map{}
}
