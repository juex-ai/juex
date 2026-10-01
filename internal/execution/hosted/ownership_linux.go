//go:build linux

package hosted

import (
	"os"
	"syscall"
)

func controlOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
