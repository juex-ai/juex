//go:build darwin || linux || freebsd || netbsd || openbsd

package tools

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

func startPTYSession(cmd *exec.Cmd, session *shellSession) (io.WriteCloser, error) {
	configureTerminalCommandForContext(cmd)
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, err
	}
	go func() {
		defer ptyFile.Close()
		_, _ = io.Copy(shellSessionWriter{session: session}, ptyFile)
	}()
	return ptyFile, nil
}
