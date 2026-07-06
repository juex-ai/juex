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
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadOnly
	got, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  func(string) (string, error) { return "/usr/bin/bwrap", nil },
	}).Prepare(context.Background(), Request{
		Policy:         policy,
		WorkspaceRoots: []string{"/work"},
		Spec:           ExecSpec{Binary: "sh", Args: []string{"-c", "echo ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(got.Args, "\x00")
	for _, want := range []string{"--ro-bind\x00/\x00/", "--dev\x00/dev", "--tmpfs\x00/tmp", "--bind\x00/work\x00/work"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %#v", want, got.Args)
		}
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
		LookPath:  func(string) (string, error) { return "/usr/bin/bwrap", nil },
	}).Prepare(context.Background(), Request{
		Policy:         policy,
		WorkspaceRoots: []string{"/work"},
		Spec:           ExecSpec{Binary: "sh", Args: []string{"-c", "echo ok"}},
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

func TestLinuxBlockedPathsRejectMissingPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-secret")
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	policy.FileSystem.BlockedPaths = []string{missing}
	_, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  func(string) (string, error) { return "/usr/bin/bwrap", nil },
	}).Prepare(context.Background(), Request{
		Policy:         policy,
		WorkspaceRoots: []string{"/work"},
		Spec:           ExecSpec{Binary: "sh"},
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
