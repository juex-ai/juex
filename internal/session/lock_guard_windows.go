//go:build windows

package session

import (
	"os"
	"syscall"
	"unsafe"
)

const lockFileExclusiveLock = 0x00000002

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

type lockGuard struct {
	file       *os.File
	overlapped syscall.Overlapped
}

func acquireLockGuard(path string) (*lockGuard, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	guard := &lockGuard{file: file}
	if err := lockFileEx(syscall.Handle(file.Fd()), lockFileExclusiveLock, 1, &guard.overlapped); err != nil {
		_ = file.Close()
		return nil, err
	}
	return guard, nil
}

func (g *lockGuard) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	unlockErr := unlockFileEx(syscall.Handle(g.file.Fd()), 1, &g.overlapped)
	closeErr := g.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func lockFileEx(handle syscall.Handle, flags uint32, bytesLow uint32, overlapped *syscall.Overlapped) error {
	r1, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		0,
		uintptr(bytesLow),
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if r1 == 0 {
		if err != syscall.Errno(0) {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}

func unlockFileEx(handle syscall.Handle, bytesLow uint32, overlapped *syscall.Overlapped) error {
	r1, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		0,
		uintptr(bytesLow),
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if r1 == 0 {
		if err != syscall.Errno(0) {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}
