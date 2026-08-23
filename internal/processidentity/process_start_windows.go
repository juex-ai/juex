//go:build windows

package processidentity

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// StartedAt returns the operating system's start time for pid.
func StartedAt(pid int) (time.Time, error) {
	creation, err := readProcessCreationTime(pid)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, creation.Nanoseconds()).UTC(), nil
}

// Fingerprint returns a stable process-incarnation identity for pid.
func Fingerprint(pid int) (string, error) {
	creation, err := readProcessCreationTime(pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:%08x:%08x", creation.HighDateTime, creation.LowDateTime), nil
}

func readProcessCreationTime(pid int) (windows.Filetime, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return windows.Filetime{}, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return windows.Filetime{}, fmt.Errorf("read process %d start time: %w", pid, err)
	}
	return creation, nil
}
