package sandbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDefaultPolicy(t *testing.T) {
	policy := DefaultPolicy()
	want := DefaultPolicyForOS(runtime.GOOS)
	if policy.Enabled != want.Enabled || policy.FileSystem.OutsideWorkspace != want.FileSystem.OutsideWorkspace || policy.Network.Enabled != want.Network.Enabled {
		t.Fatalf("policy = %+v", policy)
	}
	if len(policy.FileSystem.BlockedPaths) != 0 {
		t.Fatalf("blocked paths = %#v, want empty", policy.FileSystem.BlockedPaths)
	}
}

func filePolicyForTest(policy Policy, workDir string, roots ...string) FilePolicy {
	agentStateDir := ""
	if len(roots) > 1 {
		agentStateDir = roots[1]
	}
	return NewFilePolicy(FilePolicyOptions{Policy: policy, WorkDir: workDir, AgentStateDir: agentStateDir})
}

func sandboxLookPathForTest(backend, target string) func(string) (string, error) {
	return func(name string) (string, error) {
		switch name {
		case "bwrap", "sandbox-exec":
			return backend, nil
		case "true":
			return target, nil
		default:
			return "", fmt.Errorf("unexpected sandbox executable lookup %q", name)
		}
	}
}

func TestDefaultPolicyForOS(t *testing.T) {
	for _, tc := range []struct {
		goos    string
		enabled bool
		outside OutsideWorkspaceAccess
	}{
		{goos: "linux", enabled: true, outside: OutsideWorkspaceReadOnly},
		{goos: "darwin", enabled: true, outside: OutsideWorkspaceReadOnly},
		{goos: "windows", enabled: false, outside: OutsideWorkspaceReadWrite},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			policy := DefaultPolicyForOS(tc.goos)
			if policy.Enabled != tc.enabled || policy.FileSystem.OutsideWorkspace != tc.outside || !policy.Network.Enabled {
				t.Fatalf("policy = %+v", policy)
			}
		})
	}
}

func TestValidateOutsideWorkspaceAccessRejectsDenied(t *testing.T) {
	err := ValidateOutsideWorkspaceAccess(OutsideWorkspaceAccess("denied"))
	if err == nil || !strings.Contains(err.Error(), "read_write, read_only") {
		t.Fatalf("err = %v, want read_write/read_only enum error", err)
	}
}

func TestDefaultRunnerReturnsOriginalSpecWhenDisabled(t *testing.T) {
	spec := ExecSpec{Binary: "sh", Args: []string{"-c", "echo ok"}, Dir: "/work"}
	got, err := (DefaultRunner{RuntimeOS: "windows"}).Prepare(context.Background(), Request{
		Policy: LegacyDefaultPolicy(),
		Spec:   spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Binary != spec.Binary || strings.Join(got.Args, "\x00") != strings.Join(spec.Args, "\x00") || got.Dir != spec.Dir {
		t.Fatalf("spec = %+v, want %+v", got, spec)
	}
}

func TestLauncherEnvironmentDefersLoaderVariablesUntilSandboxedTarget(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"LD_PRELOAD=/tmp/inject.so",
		"LD_LIBRARY_PATH=/tmp/lib",
		"DYLD_INSERT_LIBRARIES=/tmp/inject.dylib",
		"GLIBC_TUNABLES=glibc.malloc.check=3",
		"SAFE=value",
		sandboxTargetEnvironmentKey + "=user-value",
	}
	got := strings.Join(launcherEnvironment(env), "\n")
	for _, forbidden := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "GLIBC_TUNABLES", sandboxTargetEnvironmentKey} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("launcher environment contains %s: %q", forbidden, got)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "SAFE=value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("launcher environment missing %s: %q", want, got)
		}
	}

	binary, args, launcher, err := sandboxTargetLaunch(ExecSpec{
		Binary: "/bin/sh",
		Args:   []string{"-c", "true"},
		Env:    env,
	})
	if err != nil {
		t.Fatal(err)
	}
	if binary == "/bin/sh" {
		t.Fatalf("target helper was not selected: binary=%q args=%#v", binary, args)
	}
	argv := strings.Join(args, "\x00")
	for _, secret := range []string{"/tmp/inject.so", "/tmp/lib", "/tmp/inject.dylib", "glibc.malloc.check=3", "user-value"} {
		if strings.Contains(argv, secret) {
			t.Fatalf("target environment value leaked into helper argv: %q", argv)
		}
	}
	if !strings.Contains(argv, sandboxTargetHelperArgument) || !strings.Contains(argv, "/bin/sh") {
		t.Fatalf("target helper argv = %#v", args)
	}
	transport := environmentValueForTest(launcher, sandboxTargetEnvironmentKey)
	deferred, err := decodeSandboxTargetEnvironment(transport)
	if err != nil {
		t.Fatal(err)
	}
	deferredText := strings.Join(deferred, "\n")
	for _, want := range []string{"LD_PRELOAD=/tmp/inject.so", "LD_LIBRARY_PATH=/tmp/lib", "DYLD_INSERT_LIBRARIES=/tmp/inject.dylib", "GLIBC_TUNABLES=glibc.malloc.check=3", sandboxTargetEnvironmentKey + "=user-value"} {
		if !strings.Contains(deferredText, want) {
			t.Fatalf("deferred target environment missing %q: %#v", want, deferred)
		}
	}
}

func environmentValueForTest(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func TestDefaultRunnerWindowsEnabledFailsClosed(t *testing.T) {
	policy := LegacyDefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	policy.Network.Enabled = false
	_, err := (DefaultRunner{RuntimeOS: "windows"}).Prepare(context.Background(), Request{
		Policy: policy,
		Spec:   ExecSpec{Binary: "cmd.exe", Args: []string{"/c", "echo ok"}},
	})
	if err == nil {
		t.Fatal("expected sandbox error")
	}
	var sandboxErr *Error
	if !errors.As(err, &sandboxErr) {
		t.Fatalf("err = %T %v, want sandbox.Error", err, err)
	}
	for _, want := range []string{"platform=windows", "file_system.outside_workspace=read_only", "network.enabled=false", "not supported"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err missing %q: %v", want, err)
		}
	}
}

func TestDefaultRunnerLinuxMissingBubblewrapFailsClosed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux backend lookup is only compiled in linux builds")
	}
	policy := LegacyDefaultPolicy()
	policy.Enabled = true
	_, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  func(string) (string, error) { return "", errors.New("missing") },
	}).Prepare(context.Background(), Request{
		Policy: policy,
		Spec:   ExecSpec{Binary: "sh", Args: []string{"-c", "echo ok"}},
	})
	if err == nil || !strings.Contains(err.Error(), "backend=bubblewrap") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want bubblewrap unavailable", err)
	}
}

func TestCheckAvailabilityCachesFunctionalProbe(t *testing.T) {
	resetBackendProbeCacheForTest()
	original := runProbeCommand
	t.Cleanup(func() {
		runProbeCommand = original
		resetBackendProbeCacheForTest()
	})
	var calls atomic.Int32
	runProbeCommand = func(context.Context, string, ...string) error {
		calls.Add(1)
		return nil
	}
	policy := DefaultPolicyForOS("linux")
	lookPath := sandboxLookPathForTest("/test/bwrap", "/test/true")
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := CheckAvailability(context.Background(), policy, "linux", lookPath); err != nil {
				t.Errorf("CheckAvailability: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("functional probe calls = %d, want 1", calls.Load())
	}
}

func TestCheckAvailabilityResolvesFunctionalProbeTarget(t *testing.T) {
	resetBackendProbeCacheForTest()
	original := runProbeCommand
	t.Cleanup(func() {
		runProbeCommand = original
		resetBackendProbeCacheForTest()
	})
	var gotHelper string
	var gotArgs []string
	runProbeCommand = func(_ context.Context, helper string, args ...string) error {
		gotHelper = helper
		gotArgs = append([]string(nil), args...)
		return nil
	}
	lookPath := func(name string) (string, error) {
		switch name {
		case "bwrap":
			return "/nix/store/bubblewrap/bin/bwrap", nil
		case "true":
			return "/nix/store/coreutils/bin/true", nil
		default:
			return "", fmt.Errorf("unexpected lookup %q", name)
		}
	}
	if err := CheckAvailability(context.Background(), DefaultPolicyForOS("linux"), "linux", lookPath); err != nil {
		t.Fatal(err)
	}
	if gotHelper != "/nix/store/bubblewrap/bin/bwrap" {
		t.Fatalf("probe helper = %q", gotHelper)
	}
	if len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != "/nix/store/coreutils/bin/true" {
		t.Fatalf("probe args = %#v, want resolved true target", gotArgs)
	}
}

func TestDefaultRunnerFunctionalProbeFailureFailsClosed(t *testing.T) {
	policy := DefaultPolicyForOS("linux")
	_, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  func(string) (string, error) { return "/test/bwrap", nil },
		Probe: func(context.Context, string, string, Policy) error {
			return fmt.Errorf("operation not permitted")
		},
	}).Prepare(context.Background(), Request{
		Policy:     policy,
		WorkDir:    "/work",
		FilePolicy: filePolicyForTest(policy, "/work", "/work"),
		Spec:       ExecSpec{Binary: "/bin/true"},
	})
	if err == nil {
		t.Fatal("expected sandbox probe error")
	}
	var sandboxErr *Error
	if !errors.As(err, &sandboxErr) || sandboxErr.Code != ErrorCodeBackendUnavailable || sandboxErr.Phase != "probe" {
		t.Fatalf("err = %T %v, want backend_unavailable probe", err, err)
	}
}
