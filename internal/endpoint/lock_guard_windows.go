//go:build windows

package endpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusive       = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

var (
	endpointKernel32     = syscall.NewLazyDLL("kernel32.dll")
	endpointLockFileEx   = endpointKernel32.NewProc("LockFileEx")
	endpointUnlockFileEx = endpointKernel32.NewProc("UnlockFileEx")
)

type lockGuard struct {
	file       *os.File
	overlapped syscall.Overlapped
}

func acquireLockGuard(path string) (*lockGuard, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	guard := &lockGuard{file: file}
	err = endpointLock(syscall.Handle(file.Fd()), lockFileExclusive|lockFileFailImmediately, &guard.overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, errorLockViolation) {
			return nil, errLockBusy
		}
		return nil, err
	}
	return guard, nil
}

func (g *lockGuard) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	unlockErr := endpointUnlock(syscall.Handle(g.file.Fd()), &g.overlapped)
	closeErr := g.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func endpointLock(handle syscall.Handle, flags uint32, overlapped *syscall.Overlapped) error {
	result, _, callErr := endpointLockFileEx.Call(
		uintptr(handle), uintptr(flags), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return syscall.EINVAL
}

func endpointUnlock(handle syscall.Handle, overlapped *syscall.Overlapped) error {
	result, _, callErr := endpointUnlockFileEx.Call(
		uintptr(handle), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return syscall.EINVAL
}

func syncDir(string) error {
	return nil
}
