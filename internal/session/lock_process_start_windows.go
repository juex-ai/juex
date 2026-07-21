//go:build windows

package session

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func processStartedAt(pid int) (time.Time, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, fmt.Errorf("read process %d start time: %w", pid, err)
	}
	return time.Unix(0, creation.Nanoseconds()).UTC(), nil
}
