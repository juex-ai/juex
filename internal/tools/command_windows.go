//go:build windows

package tools

import (
	"os"
	"os/exec"
)

func configureCommandForContext(cmd *exec.Cmd) {}

func configureTerminalCommandForContext(cmd *exec.Cmd) {}

func interruptCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Signal(os.Interrupt)
}
