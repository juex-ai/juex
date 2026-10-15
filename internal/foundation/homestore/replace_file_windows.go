//go:build windows

package homestore

import (
	"errors"
	"os"
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
	return retryReplacement(func() error {
		return moveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	})
}

func replaceFileAt(root *os.Root, tempName, targetName string) error {
	return retryReplacement(func() error { return root.Rename(tempName, targetName) })
}

func retryReplacement(replace func() error) error {
	// Readers without delete sharing can briefly block replacement. Access denied
	// can also be permanent, so bound retries while keeping the same atomic move.
	const maxRetries = 6
	delay := 5 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := replace()
		if err == nil || attempt == maxRetries ||
			(!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}
