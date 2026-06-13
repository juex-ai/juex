//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !windows

package tools

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
)

func startPTYSession(cmd *exec.Cmd, session *shellSession) (io.WriteCloser, error) {
	return nil, fmt.Errorf("shell: tty mode is not supported on %s", runtime.GOOS)
}
