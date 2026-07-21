//go:build linux

package session

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Linux exposes process start time in USER_HZ units, whose userspace ABI is
// fixed at 100 ticks per second regardless of the kernel timer frequency.
const linuxUserHZ = 100

func processStartedAt(pid int) (time.Time, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, err
	}
	closingParen := strings.LastIndexByte(string(stat), ')')
	if closingParen < 0 {
		return time.Time{}, fmt.Errorf("parse /proc/%d/stat: missing command terminator", pid)
	}
	fields := strings.Fields(string(stat[closingParen+1:]))
	// fields starts at proc field 3 (state), so field 22 (starttime) is index 19.
	if len(fields) <= 19 {
		return time.Time{}, fmt.Errorf("parse /proc/%d/stat: missing start time", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}

	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	var bootSeconds int64
	for _, line := range strings.Split(string(procStat), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		bootSeconds, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse /proc/stat boot time: %w", err)
		}
		break
	}
	if bootSeconds == 0 {
		return time.Time{}, fmt.Errorf("parse /proc/stat: missing boot time")
	}
	startDuration := time.Duration(startTicks) * time.Second / linuxUserHZ
	return time.Unix(bootSeconds, 0).Add(startDuration).UTC(), nil
}
