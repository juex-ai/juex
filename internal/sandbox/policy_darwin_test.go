//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinReadOnlyProfileRestrictsWritesOutsideWorkspace(t *testing.T) {
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	profile, err := darwinProfile(policy, []string{"/tmp/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(allow default)", "(deny file-write*", "require-not", "/tmp/workspace", "/dev/null", "/private/tmp", "/var/folders"} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing %q:\n%s", want, profile)
		}
	}
}

func TestDarwinProfileBlocksConfiguredPaths(t *testing.T) {
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	policy.FileSystem.BlockedPaths = []string{"/tmp/secret"}
	profile, err := darwinProfile(policy, []string{"/tmp/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(deny file-read* (literal \"/tmp/secret\"))",
		"(deny file-read* (subpath \"/tmp/secret\"))",
		"(deny file-write* (literal \"/tmp/secret\"))",
		"(deny file-write-unlink (subpath \"/tmp/secret\"))",
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing %q:\n%s", want, profile)
		}
	}
}

func TestDarwinBackendRestoresTargetEnvironmentInsideSandbox(t *testing.T) {
	policy := DefaultPolicy()
	policy.Enabled = true
	got, err := (DefaultRunner{
		RuntimeOS: "darwin",
		LookPath:  func(string) (string, error) { return "/usr/bin/sandbox-exec", nil },
	}).Prepare(context.Background(), Request{
		Policy: policy,
		Spec: ExecSpec{
			Binary: "/bin/sh",
			Args:   []string{"-c", "true"},
			Env:    []string{"PATH=/usr/bin", "LD_PRELOAD=/tmp/inject.dylib", "EMPTY=", "SAFE_SECRET=normal-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got.Env, "\n"), "LD_PRELOAD") {
		t.Fatalf("wrapper environment leaked loader variable: %#v", got.Env)
	}
	args := strings.Join(got.Args, "\x00")
	for _, leaked := range []string{"/tmp/inject.dylib", "normal-secret"} {
		if strings.Contains(args, leaked) {
			t.Fatalf("wrapper argv leaked environment value %q: %#v", leaked, got.Args)
		}
	}
	if strings.Contains(args, "/usr/bin/env") {
		t.Fatalf("wrapper argv still uses /usr/bin/env assignments: %#v", got.Args)
	}
	for _, want := range []string{sandboxTargetHelperArgument, "/bin/sh"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, got.Args)
		}
	}
	if !strings.Contains(strings.Join(got.Env, "\n"), "SAFE_SECRET=normal-secret") {
		t.Fatalf("wrapper environment lost safe target value: %#v", got.Env)
	}
}

func TestDarwinReadOnlyBackendAllowsWorkspaceWriteOnly(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside, err := os.MkdirTemp(cwd, ".sandbox-outside-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(outside) }()
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	spec, err := (DefaultRunner{RuntimeOS: "darwin"}).Prepare(context.Background(), Request{
		Policy:         policy,
		WorkspaceRoots: []string{work},
		Spec: ExecSpec{
			Binary: "sh",
			Args: []string{
				"-c",
				"echo ok > " + shellPath(filepath.Join(work, "inside")) +
					"; echo bad > " + shellPath(filepath.Join(outside, "outside")) + " 2>/dev/null && exit 7; exit 0",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(spec.Binary, spec.Args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed command failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(work, "inside")); err != nil {
		t.Fatalf("workspace write missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "outside")); !os.IsNotExist(err) {
		t.Fatalf("outside write = %v, want denied/missing", err)
	}
}

func TestDarwinReadOnlyBackendAllowsDeviceAndTempWrites(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	work := t.TempDir()
	tempPath := filepath.Join(os.TempDir(), "juex-sandbox-temp-"+strings.ReplaceAll(t.Name(), "/", "-"))
	defer func() { _ = os.Remove(tempPath) }()
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	spec, err := (DefaultRunner{RuntimeOS: "darwin"}).Prepare(context.Background(), Request{
		Policy:         policy,
		WorkspaceRoots: []string{work},
		Spec: ExecSpec{
			Binary: "sh",
			Args: []string{
				"-c",
				"echo ok >/dev/null; echo ok > " + shellPath(tempPath),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(spec.Binary, spec.Args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed command failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("temp write missing: %v", err)
	}
}

func shellPath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
