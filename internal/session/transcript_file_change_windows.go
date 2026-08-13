//go:build windows

package session

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

type windowsFileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

func transcriptFileChangeID(file *os.File, _ os.FileInfo) string {
	handle := windows.Handle(file.Fd())
	var basic windowsFileBasicInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)),
		uint32(unsafe.Sizeof(basic)),
	); err != nil || basic.ChangeTime == 0 {
		return ""
	}

	var id windowsFileIDInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&id)),
		uint32(unsafe.Sizeof(id)),
	); err == nil {
		return fmt.Sprintf("windows:%x:%x:%x", id.VolumeSerialNumber, id.FileID, basic.ChangeTime)
	}

	var fallback windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fallback); err != nil {
		return ""
	}
	return fmt.Sprintf("windows:%x:%x:%x:%x", fallback.VolumeSerialNumber, fallback.FileIndexHigh,
		fallback.FileIndexLow, basic.ChangeTime)
}
