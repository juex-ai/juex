//go:build linux || darwin

package blob

import (
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"golang.org/x/sys/unix"
	"os"
)

func lockRoot(root *os.Root) (*os.File, error) {
	file, err := root.OpenFile(".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if os.IsExist(err) {
		file, err = regular(root, ".lock", os.O_RDWR)
	}
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, execprotocol.ErrConflict
	}
	return file, nil
}
