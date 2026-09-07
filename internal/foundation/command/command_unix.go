//go:build darwin || linux || freebsd || netbsd || openbsd

package command

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func ConfigureContext(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	configureCommandCancel(cmd)
}

func ConfigureTerminalContext(cmd *exec.Cmd) {
	// The PTY package starts the child as a new session leader with a
	// controlling terminal. Do not also set Setpgid here; Setsid and Setpgid
	// conflict on Unix.
	configureCommandCancel(cmd)
}

func configureCommandCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return signalCommandProcessGroup(cmd, syscall.SIGKILL)
	}
}

func InterruptProcessGroup(cmd *exec.Cmd) error {
	return signalCommandProcessGroup(cmd, syscall.SIGINT)
}

func signalCommandProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
