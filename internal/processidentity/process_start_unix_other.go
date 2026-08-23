//go:build freebsd || netbsd || openbsd || dragonfly || solaris

package processidentity

import (
	"fmt"
	"time"
)

// StartedAt reports that this platform does not expose a supported process
// start identity.
func StartedAt(pid int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("process start time is unavailable for pid %d on this platform", pid)
}
