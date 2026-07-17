//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package endpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type lockGuard struct {
	file *os.File
}

func acquireLockGuard(path string) (*lockGuard, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errLockBusy
		}
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

func syncDir(path string) error {
	return syncDirWith(path, func(dir *os.File) error { return dir.Sync() })
}

func syncDirWith(path string, syncFile func(*os.File) error) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	err = syncFile(dir)
	if errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.ENOSYS) {
		return nil
	}
	return err
}
