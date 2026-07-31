package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultPolicy(t *testing.T) {
	policy := DefaultPolicy()
	if policy.Enabled {
		t.Fatalf("enabled = true, want false")
	}
	if policy.FileSystem.OutsideWorkspace != OutsideWorkspaceReadWrite || !policy.Network.Enabled {
		t.Fatalf("policy = %+v", policy)
	}
	if len(policy.FileSystem.BlockedPaths) != 0 {
		t.Fatalf("blocked paths = %#v, want empty", policy.FileSystem.BlockedPaths)
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
		Policy: DefaultPolicy(),
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
	policy := DefaultPolicy()
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
	policy := DefaultPolicy()
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

func TestDefaultRunnerRejectsAdditionalWritableRootBlockedPathOverlap(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "agent", "extensions", "demo")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	tests := []struct {
		name    string
		blocked string
	}{
		{name: "blocked ancestor", blocked: filepath.Dir(dataDir)},
		{name: "blocked exact", blocked: dataDir},
		{name: "blocked descendant", blocked: filepath.Join(dataDir, "secret")},
		{name: "relative blocked ancestor", blocked: filepath.Join("agent", "extensions")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := policy
			policy.FileSystem.BlockedPaths = []string{tc.blocked}
			_, err := (DefaultRunner{
				RuntimeOS: "darwin",
				LookPath:  func(string) (string, error) { return "/usr/bin/sandbox-exec", nil },
			}).Prepare(context.Background(), Request{
				Policy:                  policy,
				WorkspaceRoots:          []string{root},
				AdditionalWritableRoots: []string{dataDir},
				Spec:                    ExecSpec{Binary: "/bin/true"},
			})
			if err == nil || !strings.Contains(err.Error(), "additional writable root") || !strings.Contains(err.Error(), "blocked_paths") {
				t.Fatalf("Prepare() error = %v, want writable-root conflict", err)
			}
		})
	}
}

func TestDefaultRunnerRejectsSymlinkedAdditionalWritableRootBlockedOverlap(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	logical := filepath.Join(root, "logical")
	if err := os.Symlink(physical, logical); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	policy.FileSystem.BlockedPaths = []string{physical}
	_, err := (DefaultRunner{
		RuntimeOS: "darwin",
		LookPath:  func(string) (string, error) { return "/usr/bin/sandbox-exec", nil },
	}).Prepare(context.Background(), Request{
		Policy:                  policy,
		WorkspaceRoots:          []string{root},
		AdditionalWritableRoots: []string{logical},
		Spec:                    ExecSpec{Binary: "/bin/true"},
	})
	if err == nil || !strings.Contains(err.Error(), "additional writable root") {
		t.Fatalf("Prepare() error = %v, want physical-path conflict", err)
	}
}

func TestDefaultRunnerDarwinAllowsOnlyExactAdditionalWritableRoot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	dataRoot := filepath.Join(root, "agent", "extensions")
	dataDir := filepath.Join(dataRoot, "demo")
	sibling := filepath.Join(dataRoot, "other")
	for _, path := range []string{workspace, dataDir, sibling} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	got, err := (DefaultRunner{
		RuntimeOS: "darwin",
		LookPath:  func(string) (string, error) { return "/usr/bin/sandbox-exec", nil },
	}).Prepare(context.Background(), Request{
		Policy:                  policy,
		WorkspaceRoots:          []string{workspace},
		AdditionalWritableRoots: []string{dataDir},
		Spec:                    ExecSpec{Binary: "/bin/true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := strings.Join(got.Args, "\n")
	normalized := normalizedRoots([]string{workspace, dataDir})
	for _, want := range []string{
		`(subpath "` + normalized[0] + `")`,
		`(subpath "` + normalized[1] + `")`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing %q:\n%s", want, profile)
		}
	}
	for _, forbidden := range []string{
		`(subpath "` + dataRoot + `")`,
		`(subpath "` + sibling + `")`,
	} {
		if strings.Contains(profile, forbidden) {
			t.Fatalf("profile grants forbidden root %q:\n%s", forbidden, profile)
		}
	}
}
