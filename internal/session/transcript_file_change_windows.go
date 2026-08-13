//go:build windows

package session

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

func transcriptFileChangeID(file *os.File, _ os.FileInfo) string {
	handle := windows.Handle(file.Fd())
	usn, err := readFileUSN(handle)
	if err != nil {
		return ""
	}

	var id windowsFileIDInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&id)),
		uint32(unsafe.Sizeof(id)),
	); err == nil {
		return fmt.Sprintf("windows-usn:%x:%x:%x", id.VolumeSerialNumber, id.FileID, usn)
	}

	var fallback windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fallback); err != nil {
		return ""
	}
	return fmt.Sprintf("windows-usn:%x:%x:%x:%x", fallback.VolumeSerialNumber, fallback.FileIndexHigh,
		fallback.FileIndexLow, usn)
}

func readFileUSN(handle windows.Handle) (int64, error) {
	input := struct {
		MinMajorVersion uint16
		MaxMajorVersion uint16
	}{MinMajorVersion: 2, MaxMajorVersion: 3}
	var output [16]uint64
	var returned uint32
	if err := windows.DeviceIoControl(
		handle,
		windows.FSCTL_READ_FILE_USN_DATA,
		(*byte)(unsafe.Pointer(&input)),
		uint32(unsafe.Sizeof(input)),
		(*byte)(unsafe.Pointer(&output[0])),
		uint32(unsafe.Sizeof(output)),
		&returned,
		nil,
	); err != nil {
		return 0, err
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(&output[0])), int(returned))
	if len(bytes) < 8 {
		return 0, fmt.Errorf("session: short USN record: %d bytes", returned)
	}
	var usnOffset int
	major := binary.LittleEndian.Uint16(bytes[4:6])
	switch major {
	case 2:
		usnOffset = 24
	case 3:
		usnOffset = 40
	default:
		return 0, fmt.Errorf("session: unsupported USN record version: %d", major)
	}
	if len(bytes) < usnOffset+8 {
		return 0, fmt.Errorf("session: short USN record version %d: %d bytes", major, returned)
	}
	return int64(binary.LittleEndian.Uint64(bytes[usnOffset : usnOffset+8])), nil
}
