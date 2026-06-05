//go:build darwin || linux || freebsd || netbsd || openbsd

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureCommandForContextPreservesExistingSysProcAttr(t *testing.T) {
	cmd := exec.Command("bash", "-c", "true")
	attr := &syscall.SysProcAttr{}
	cmd.SysProcAttr = attr

	configureCommandForContext(cmd)

	if cmd.SysProcAttr != attr {
		t.Fatal("configureCommandForContext replaced existing SysProcAttr")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("configureCommandForContext did not enable Setpgid")
	}
}

func TestConfigureCommandForContextRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		cmd := exec.Command("bash", "-c", "true")
		configureCommandForContext(cmd)
		cmd.Process = &os.Process{Pid: pid}

		if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("Cancel with pid %d returned %v, want os.ErrProcessDone", pid, err)
		}
	}
}
