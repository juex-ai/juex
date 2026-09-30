//go:build linux || darwin

package native

import (
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another executor owns this state directory")
	}
	return file, nil
}

func openRegular(path string, flags int, permission os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, flags|unix.O_NONBLOCK, permission)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("file tools require a regular file")
	}
	return file, nil
}

func startTerminal(cmd *exec.Cmd, output io.Writer) (io.WriteCloser, <-chan struct{}, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(output, terminal) }()
	return terminal, done, nil
}
