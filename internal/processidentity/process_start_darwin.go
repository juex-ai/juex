//go:build darwin

package processidentity

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StartedAt returns the operating system's start time for pid.
func StartedAt(pid int) (time.Time, error) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, fmt.Errorf("read process %d start time: %w", pid, err)
	}
	sec, nsec := proc.Proc.P_starttime.Unix()
	return time.Unix(sec, nsec).UTC(), nil
}
