//go:build !windows

package endpoint

import "os"

func replaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}
