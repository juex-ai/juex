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

// Fingerprint reports that this platform does not expose a supported process
// incarnation identity.
func Fingerprint(pid int) (string, error) {
	return "", fmt.Errorf("process identity is unavailable for pid %d on this platform", pid)
}
