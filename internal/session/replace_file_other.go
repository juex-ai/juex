//go:build !windows

package session

import "os"

func replaceFile(tmpName, path string) error {
	return os.Rename(tmpName, path)
}
