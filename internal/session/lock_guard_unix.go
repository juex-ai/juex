//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package session

import (
	"os"
	"syscall"
)

type lockGuard struct {
	file *os.File
}

func acquireLockGuard(path string) (*lockGuard, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &lockGuard{file: file}, nil
}

func (g *lockGuard) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(g.file.Fd()), syscall.LOCK_UN)
	closeErr := g.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
