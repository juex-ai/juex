//go:build freebsd || netbsd || openbsd || dragonfly || solaris

package session

import (
	"fmt"
	"time"
)

func processStartedAt(pid int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("process start time is unavailable for pid %d on this platform", pid)
}
