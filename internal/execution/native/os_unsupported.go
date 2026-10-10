//go:build !linux && !darwin

package native

import (
	"errors"
	"io"
	"os"
	"os/exec"
)

func fileIdentity(os.FileInfo) string { return "" }

func acquireLock(string) (*os.File, error) {
	return nil, errors.New("native execution requires Linux or macOS")
}

func openRegular(string, int, os.FileMode) (*os.File, error) {
	return nil, errors.New("native file execution requires Linux or macOS")
}
func openRootRegular(*os.Root, string) (*os.File, error) {
	return nil, errors.New("native file execution requires Linux or macOS")
}
func startTerminal(*exec.Cmd, io.Writer) (io.WriteCloser, <-chan struct{}, error) {
	return nil, nil, errors.New("PTY execution requires Linux or macOS")
}
