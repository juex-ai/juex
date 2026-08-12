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
	profile, err := darwinProfile(policy, "/tmp/workspace", []string{"/tmp/workspace"})
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
	profile, err := darwinProfile(policy, "/tmp/workspace", []string{"/tmp/workspace"})
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
		Policy:        policy,
		WorkDir:       work,
		WritableRoots: []string{work},
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
		Policy:        policy,
		WorkDir:       work,
		WritableRoots: []string{work},
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

func TestDefaultRunnerDarwinAllowsWorkspaceAndAgentStateRoots(t *testing.T) {
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
		Policy:        policy,
		WorkDir:       workspace,
		WritableRoots: []string{workspace, dataDir},
		Spec:          ExecSpec{Binary: "/bin/true"},
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

func TestDarwinProfileLetsBlockedPathsOverrideAgentWritableRoot(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "agent", "extensions", "demo")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	for _, blocked := range []string{
		filepath.Dir(dataDir),
		dataDir,
		filepath.Join(dataDir, "secret"),
		filepath.Join("agent", "extensions"),
	} {
		policy := policy
		policy.FileSystem.BlockedPaths = []string{blocked}
		profile, err := darwinProfile(policy, root, []string{root, dataDir})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(profile, "deny file-read") || !strings.Contains(profile, filepath.Clean(blocked)) {
			t.Fatalf("profile does not let blocked path %q override writable root:\n%s", blocked, profile)
		}
	}
}

func TestDarwinProfileResolvesSymlinkedBlockedPathInsideWritableRoot(t *testing.T) {
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
	profile, err := darwinProfile(policy, root, []string{root, logical})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, physical) {
		t.Fatalf("profile does not contain physical blocked path %q:\n%s", physical, profile)
	}
}

func TestDarwinProfileRelativeBlockedPathUsesWorkDirNotWritableRootOrder(t *testing.T) {
	workspace := t.TempDir()
	agentStateDir := t.TempDir()
	blocked := filepath.Join(workspace, "secret")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	policy.FileSystem.BlockedPaths = []string{"secret"}
	profile, err := darwinProfile(policy, workspace, []string{agentStateDir, workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile, blocked) {
		t.Fatalf("relative blocked path did not resolve from WorkDir:\n%s", profile)
	}
	if wrong := filepath.Join(agentStateDir, "secret"); strings.Contains(profile, wrong) {
		t.Fatalf("relative blocked path resolved from WritableRoots order: %s", wrong)
	}
}

func shellPath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
