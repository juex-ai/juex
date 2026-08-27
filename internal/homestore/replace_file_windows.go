//go:build windows

package homestore

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

var moveFileEx = windows.MoveFileEx

func replaceFile(tempPath, targetPath string) error {
	from, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	// Readers without delete sharing can briefly block replacement. Access denied
	// can also be permanent, so bound retries while keeping the same atomic move.
	const maxRetries = 6
	delay := 5 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err = moveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil || attempt == maxRetries ||
			(!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}
