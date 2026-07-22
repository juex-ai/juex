//go:build windows

package homestore

import "golang.org/x/sys/windows"

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
	return moveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
