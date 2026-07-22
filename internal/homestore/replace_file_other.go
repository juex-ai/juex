//go:build !windows

package homestore

import "os"

func replaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}
