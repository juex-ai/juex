//go:build windows

package command

import (
	"os"
	"os/exec"
)

func ConfigureContext(cmd *exec.Cmd) {}

func ConfigureTerminalContext(cmd *exec.Cmd) {}

func InterruptProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Signal(os.Interrupt); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
