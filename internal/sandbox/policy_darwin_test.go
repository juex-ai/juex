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
	defer os.RemoveAll(outside)
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
	defer os.Remove(tempPath)
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
