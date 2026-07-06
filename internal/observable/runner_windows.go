//go:build windows

package observable

import "os/exec"

func configureObservableCommand(cmd *exec.Cmd) {
	_ = cmd
}
