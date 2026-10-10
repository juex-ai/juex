//go:build darwin || linux

package migration

import (
	"os"
	"syscall"
)

func openBundleFile(root *os.Root, name string) (*os.File, error) {
	// A regular file can be replaced after Lstat. Never block on a replacement
	// FIFO or follow a replacement symlink before checking the opened inode.
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
