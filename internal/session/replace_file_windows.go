//go:build windows

package session

import (
	"errors"
	"os"
	"time"
)

const (
	windowsReplaceTimeout = 2 * time.Second
	windowsReplacePoll    = 10 * time.Millisecond
)

func replaceFile(tmpName, path string) error {
	deadline := time.Now().Add(windowsReplaceTimeout)
	for {
		err := replaceFileOnce(tmpName, path)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		// Readers and virus scanners can briefly hold handles without
		// FILE_SHARE_DELETE, so Windows replacement must tolerate that race.
		time.Sleep(windowsReplacePoll)
	}
}

func replaceFileOnce(tmpName, path string) error {
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpName, path)
}
