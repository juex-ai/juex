//go:build windows

package session

import (
	"errors"
	"syscall"
)

const (
	waitObject0           = 0x00000000
	waitTimeout           = 0x00000102
	errorInvalidParameter = syscall.Errno(87)
)

func processExists(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, errorInvalidParameter) {
			return false, nil
		}
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(handle)

	event, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	switch event {
	case waitTimeout:
		return true, nil
	case waitObject0:
		return false, nil
	default:
		return true, nil
	}
}
