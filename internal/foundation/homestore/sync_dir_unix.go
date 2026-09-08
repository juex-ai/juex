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

// SyncRoot applies the platform directory-sync policy through the opened root.
func SyncRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return directorySyncError(dir.Sync())
}

func syncDirWith(path string, syncFile func(*os.File) error) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return directorySyncError(syncFile(dir))
}

func directorySyncError(err error) error {
	if errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.ENOSYS) {
		return nil
	}
	return err
}
