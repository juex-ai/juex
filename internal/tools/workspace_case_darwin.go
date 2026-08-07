//go:build darwin

package tools

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const volumeCapabilityCaseSensitive = 0x00000100

type volumeCapabilitiesResult struct {
	Length       uint32
	Capabilities [4]uint32
	Valid        [4]uint32
}

func workspaceCaseInsensitive(path string) bool {
	pathPointer, err := unix.BytePtrFromString(path)
	if err != nil {
		return false
	}
	attributes := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Volattr:     unix.ATTR_VOL_INFO | unix.ATTR_VOL_CAPABILITIES,
	}
	var result volumeCapabilitiesResult
	_, _, errno := unix.Syscall6(
		unix.SYS_GETATTRLIST, //nolint:staticcheck // x/sys has no read-only getattrlist wrapper.
		uintptr(unsafe.Pointer(pathPointer)),
		uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&result)),
		unsafe.Sizeof(result),
		0,
		0,
	)
	runtime.KeepAlive(pathPointer)
	runtime.KeepAlive(attributes)
	runtime.KeepAlive(result)
	if errno != 0 || uintptr(result.Length) < unsafe.Sizeof(result) {
		return false
	}
	if result.Valid[0]&volumeCapabilityCaseSensitive == 0 {
		return false
	}
	return result.Capabilities[0]&volumeCapabilityCaseSensitive == 0
}
