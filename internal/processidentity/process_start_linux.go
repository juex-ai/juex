//go:build linux

package processidentity

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

// StartedAt returns the operating system's start time for pid.
func StartedAt(pid int) (time.Time, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, err
	}
	startTicks, err := parseLinuxProcessStartTicks(pid, stat)
	if err != nil {
		return time.Time{}, err
	}
	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	return linuxProcessStartedAt(startTicks, procStat)
}

// Fingerprint returns a stable process-incarnation identity for pid. It uses
// kernel-relative start ticks and the boot ID, so wall-clock adjustments cannot
// change the identity of a running process.
func Fingerprint(pid int) (string, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	startTicks, err := parseLinuxProcessStartTicks(pid, stat)
	if err != nil {
		return "", err
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(bootID))
	if identity == "" {
		return "", fmt.Errorf("parse Linux boot ID: empty value")
	}
	return formatLinuxFingerprint(identity, startTicks), nil
}

func parseLinuxProcessStartTicks(pid int, stat []byte) (uint64, error) {
	closingParen := strings.LastIndexByte(string(stat), ')')
	if closingParen < 0 {
		return 0, fmt.Errorf("parse /proc/%d/stat: missing command terminator", pid)
	}
	fields := strings.Fields(string(stat[closingParen+1:]))
	// fields starts at proc field 3 (state), so field 22 (starttime) is index 19.
	if len(fields) <= 19 {
		return 0, fmt.Errorf("parse /proc/%d/stat: missing start time", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return startTicks, nil
}

func linuxProcessStartedAt(startTicks uint64, procStat []byte) (time.Time, error) {
	var bootSeconds int64
	for _, line := range strings.Split(string(procStat), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		parsedBootSeconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse /proc/stat boot time: %w", err)
		}
		bootSeconds = parsedBootSeconds
		break
	}
	if bootSeconds == 0 {
		return time.Time{}, fmt.Errorf("parse /proc/stat: missing boot time")
	}
	startDuration := time.Duration(startTicks) * time.Second / linuxUserHZ
	return time.Unix(bootSeconds, 0).Add(startDuration).UTC(), nil
}

func formatLinuxFingerprint(bootID string, startTicks uint64) string {
	return fmt.Sprintf("linux:%s:%d", bootID, startTicks)
}
