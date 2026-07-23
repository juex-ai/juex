//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package homestore

import (
	"errors"
	"os"
	"syscall"
)

func SyncDir(path string) error {
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
