//go:build windows

package fleet

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
	fleetKernel32     = syscall.NewLazyDLL("kernel32.dll")
	fleetLockFileEx   = fleetKernel32.NewProc("LockFileEx")
	fleetUnlockFileEx = fleetKernel32.NewProc("UnlockFileEx")
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
	result, _, callErr := fleetLockFileEx.Call(
		uintptr(syscall.Handle(file.Fd())),
		uintptr(lockFileExclusive|lockFileFailImmediately),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&guard.overlapped)),
	)
	if result == 0 {
		_ = file.Close()
		if errors.Is(callErr, errorLockViolation) {
			return nil, errLockBusy
		}
		if callErr != syscall.Errno(0) {
			return nil, callErr
		}
		return nil, syscall.EINVAL
	}
	return guard, nil
}

func (g *lockGuard) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	result, _, callErr := fleetUnlockFileEx.Call(
		uintptr(syscall.Handle(g.file.Fd())),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&g.overlapped)),
	)
	closeErr := g.file.Close()
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return syscall.EINVAL
	}
	return closeErr
}
