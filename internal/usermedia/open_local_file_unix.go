//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package usermedia

import (
	"os"

	"golang.org/x/sys/unix"
)

func openLocalFile(path string) (*os.File, error) {
	// O_NONBLOCK closes the stat/open FIFO swap window; regular files ignore it.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
