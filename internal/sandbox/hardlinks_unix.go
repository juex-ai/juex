//go:build darwin || linux

package sandbox

import (
	"os"
	"syscall"
)

func readHardLinkMetadata(path string) (hardLinkMetadata, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return hardLinkMetadata{}, false, nil
	}
	if err != nil {
		return hardLinkMetadata{}, false, err
	}
	if !info.Mode().IsRegular() {
		return hardLinkMetadata{}, false, nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink <= 1 {
		return hardLinkMetadata{}, false, nil
	}
	return hardLinkMetadata{
		identity: hardLinkIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)},
		links:    uint64(stat.Nlink),
	}, true, nil
}
