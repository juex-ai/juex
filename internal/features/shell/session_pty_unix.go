//go:build darwin || linux || freebsd || netbsd || openbsd

package shell

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
	"github.com/juex-ai/juex/internal/foundation/command"
)

func startPTYSession(cmd *exec.Cmd, session *shellSession) (io.WriteCloser, error) {
	command.ConfigureTerminalContext(cmd)
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, err
	}
	outputDone := make(chan struct{})
	session.outputDone = outputDone
	go func() {
		defer close(outputDone)
		defer ptyFile.Close()
		_, _ = io.Copy(shellSessionWriter{session: session}, ptyFile)
	}()
	return ptyFile, nil
}
