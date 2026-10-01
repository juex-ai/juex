//go:build linux || darwin

package maintenance

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func sharedLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrDraining
	}
	return err
}

func exclusiveLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrDraining
	}
	return err
}
