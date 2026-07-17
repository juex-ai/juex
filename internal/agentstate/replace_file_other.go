//go:build !windows

package agentstate

import "os"

func replaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}
