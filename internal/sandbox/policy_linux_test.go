//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLinuxReadOnlyProvidesWritableDevicesAndTemp(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	agentStateDir := filepath.Join(root, "agent-state")
	for _, path := range []string{workspace, agentStateDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	got, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  sandboxLookPathForTest("/usr/bin/bwrap", "/bin/true"),
	}).Prepare(context.Background(), Request{
		Policy:     policy,
		WorkDir:    workspace,
		FilePolicy: filePolicyForTest(policy, workspace, workspace, agentStateDir),
		Spec:       ExecSpec{Binary: "sh", Args: []string{"-c", "echo ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(got.Args, "\x00")
	for _, want := range []string{"--ro-bind\x00/\x00/", "--dev\x00/dev", "--bind\x00" + workspace + "\x00" + workspace, "--bind\x00" + agentStateDir + "\x00" + agentStateDir} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, got.Args)
		}
	}
	for _, forbidden := range []string{"--tmpfs\x00/tmp", "--dir\x00/tmp/juex"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("args unexpectedly replace host temp path %q: %#v", forbidden, got.Args)
		}
	}
	scratch := filepath.Join(agentStateDir, "tmp")
	for _, want := range []string{
		"TMPDIR=" + scratch,
		"XDG_CACHE_HOME=" + filepath.Join(scratch, "cache"),
		"GOCACHE=" + filepath.Join(scratch, "cache", "go-build"),
		"GOMODCACHE=" + filepath.Join(scratch, "cache", "go-mod"),
	} {
		if !strings.Contains(strings.Join(got.Env, "\n"), want) {
			t.Fatalf("sandbox environment missing %q: %#v", want, got.Env)
		}
	}
	if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
		t.Fatalf("scratch directory = %q: info=%v err=%v", scratch, info, err)
	}
}

func TestLinuxReadOnlyBindsWorkspaceAndAgentStateRoots(t *testing.T) {
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
		RuntimeOS: "linux",
		LookPath:  sandboxLookPathForTest("/usr/bin/bwrap", "/bin/true"),
	}).Prepare(context.Background(), Request{
		Policy:     policy,
		WorkDir:    workspace,
		FilePolicy: filePolicyForTest(policy, workspace, workspace, dataDir),
		Spec:       ExecSpec{Binary: "/bin/true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(got.Args, "\x00")
	for _, want := range []string{
		"--bind\x00" + workspace + "\x00" + workspace,
		"--bind\x00" + dataDir + "\x00" + dataDir,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, got.Args)
		}
	}
	for _, forbidden := range []string{
		"--bind\x00" + dataRoot + "\x00" + dataRoot,
		"--bind\x00" + sibling + "\x00" + sibling,
	} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("args grant forbidden root %q: %#v", forbidden, got.Args)
		}
	}
}

func TestLinuxBackendRestoresTargetEnvironmentInsideSandbox(t *testing.T) {
	policy := LegacyDefaultPolicy()
	policy.Enabled = true
	got, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  sandboxLookPathForTest("/usr/bin/bwrap", "/bin/true"),
	}).Prepare(context.Background(), Request{
		Policy: policy,
		Spec: ExecSpec{
			Binary: "/bin/sh",
			Args:   []string{"-c", "true"},
			Env:    []string{"PATH=/usr/bin", "LD_PRELOAD=/tmp/inject.so", "EMPTY=", "SAFE_SECRET=normal-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got.Env, "\n"), "LD_PRELOAD") {
		t.Fatalf("wrapper environment leaked loader variable: %#v", got.Env)
	}
	args := strings.Join(got.Args, "\x00")
	for _, leaked := range []string{"/tmp/inject.so", "normal-secret"} {
		if strings.Contains(args, leaked) {
			t.Fatalf("wrapper argv leaked environment value %q: %#v", leaked, got.Args)
		}
	}
	if strings.Contains(args, "--setenv") {
		t.Fatalf("wrapper argv still uses --setenv: %#v", got.Args)
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

func TestLinuxBlockedPathsAreMasked(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret-file")
	if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	policy.FileSystem.BlockedPaths = []string{dir, file}
	got, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  sandboxLookPathForTest("/usr/bin/bwrap", "/bin/true"),
	}).Prepare(context.Background(), Request{
		Policy:     policy,
		WorkDir:    "/work",
		FilePolicy: filePolicyForTest(policy, "/work", "/work"),
		Spec:       ExecSpec{Binary: "sh", Args: []string{"-c", "echo ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(got.Args, "\x00")
	for _, want := range []string{"--dev-bind\x00/\x00/", "--ro-bind", dir, file} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, got.Args)
		}
	}
}

func TestLinuxBlockedPathMaskFollowsWritableAgentStateBind(t *testing.T) {
	workspace := t.TempDir()
	agentStateDir := t.TempDir()
	blocked := filepath.Join(agentStateDir, "private")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	policy.FileSystem.BlockedPaths = []string{blocked}
	got, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  sandboxLookPathForTest("/usr/bin/bwrap", "/bin/true"),
	}).Prepare(context.Background(), Request{
		Policy:     policy,
		WorkDir:    workspace,
		FilePolicy: filePolicyForTest(policy, workspace, workspace, agentStateDir),
		Spec:       ExecSpec{Binary: "/bin/true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(got.Args, "\x00")
	bind := "--bind\x00" + agentStateDir + "\x00" + agentStateDir
	mask := "--ro-bind\x00"
	bindAt, maskAt := strings.Index(args, bind), strings.LastIndex(args, mask)
	if bindAt < 0 || maskAt < 0 || maskAt < bindAt || !strings.Contains(args[maskAt:], blocked) {
		t.Fatalf("blocked mask must follow AgentStateDir bind: %#v", got.Args)
	}
}

func TestLinuxBlockedPathsRejectMissingPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-secret")
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	policy.FileSystem.BlockedPaths = []string{missing}
	_, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  sandboxLookPathForTest("/usr/bin/bwrap", "/bin/true"),
	}).Prepare(context.Background(), Request{
		Policy:     policy,
		WorkDir:    "/work",
		FilePolicy: filePolicyForTest(policy, "/work", "/work"),
		Spec:       ExecSpec{Binary: "sh"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want missing blocked path error", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("missing blocked path was created on host, stat err=%v", statErr)
	}
}

func TestLinuxMaskSourcesAreReusable(t *testing.T) {
	emptyDir, emptyFile, err := linuxMaskSources()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := linuxMaskSources(); err != nil {
		t.Fatalf("second linuxMaskSources call failed: %v", err)
	}
	dirInfo, err := os.Stat(emptyDir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirInfo.IsDir() {
		t.Fatalf("empty dir source is not a directory: %s", emptyDir)
	}
	fileInfo, err := os.Stat(emptyFile)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.IsDir() || fileInfo.Size() != 0 {
		t.Fatalf("empty file source invalid: isDir=%v size=%d", fileInfo.IsDir(), fileInfo.Size())
	}
}

func TestLinuxMaskSourcesAreConcurrent(t *testing.T) {
	const calls = 16
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := linuxMaskSources()
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("linuxMaskSources concurrent call failed: %v", err)
		}
	}
}
