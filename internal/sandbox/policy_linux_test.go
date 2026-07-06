//go:build linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"
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

func TestLinuxDeniedBindsNetworkConfigWhenEnabled(t *testing.T) {
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.OutsideWorkspace = OutsideWorkspaceDenied
	policy.Network.Enabled = true
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
	if !strings.Contains(args, "--dev\x00/dev") || !strings.Contains(args, "--tmpfs\x00/tmp") {
		t.Fatalf("args missing writable device/temp mounts: %#v", got.Args)
	}
	if _, statErr := os.Stat("/etc/resolv.conf"); statErr == nil && !strings.Contains(args, "/etc/resolv.conf") {
		t.Fatalf("args missing DNS config bind: %#v", got.Args)
	}
	if _, err := (DefaultRunner{
		RuntimeOS: "linux",
		LookPath:  func(string) (string, error) { return "", errors.New("missing") },
	}).Prepare(context.Background(), Request{
		Policy:         policy,
		WorkspaceRoots: []string{"/work"},
		Spec:           ExecSpec{Binary: "sh"},
	}); err == nil {
		t.Fatal("expected missing bwrap to fail closed")
	}
}
