package sandbox

import (
	"context"
	"errors"
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
