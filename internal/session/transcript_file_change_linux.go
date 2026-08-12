//go:build linux

package session

import (
	"fmt"
	"os"
	"syscall"
)

func transcriptFileChangeID(_ *os.File, info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("linux:%x:%x:%d:%d", uint64(stat.Dev), stat.Ino, stat.Ctim.Sec, stat.Ctim.Nsec)
}
