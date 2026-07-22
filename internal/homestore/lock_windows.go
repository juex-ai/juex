//go:build windows

package homestore

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type Lock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireLock(path string, mode LockMode) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	guard := &Lock{file: file}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if mode == LockTry {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &guard.overlapped); err != nil {
		_ = file.Close()
		if mode == LockTry && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLockBusy
		}
		return nil, err
	}
	return guard, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
