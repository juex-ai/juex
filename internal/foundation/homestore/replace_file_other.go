//go:build !windows

package homestore

import "os"

func replaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}

func replaceFileAt(root *os.Root, tempName, targetName string) error {
	return root.Rename(tempName, targetName)
}
