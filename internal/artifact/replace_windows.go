//go:build windows

package artifact

import (
	"bytes"
	"errors"
	"os"
	"time"
)

const (
	windowsReplaceTimeout = 2 * time.Second
	windowsReplacePoll    = 10 * time.Millisecond
)

func replaceArtifact(root *os.Root, temp, target string, data []byte) error {
	deadline := time.Now().Add(windowsReplaceTimeout)
	var lastErr error
	for {
		if err := root.Rename(temp, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if existing, err := root.ReadFile(target); err == nil && bytes.Equal(existing, data) {
			return nil
		}
		if err := root.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		} else if err := root.Rename(temp, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(windowsReplacePoll)
	}
}
