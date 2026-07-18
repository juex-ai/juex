//go:build !windows

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func processTCPListeners(pid int) ([]string, error) {
	output, err := exec.Command(
		"lsof",
		"-nP",
		"-a",
		"-p",
		strconv.Itoa(pid),
		"-iTCP",
		"-sTCP:LISTEN",
	).Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: lsof is not installed", errProcessListenerScanUnavailable)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(output) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("scan TCP listeners with lsof: %w", err)
	}
	return parseLsofTCPListeners(string(output)), nil
}

func parseLsofTCPListeners(output string) []string {
	var listeners []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		for index, field := range fields {
			if field == "TCP" && index+1 < len(fields) {
				listeners = append(listeners, fields[index+1])
				break
			}
		}
	}
	return listeners
}

func TestParseLsofTCPListeners(t *testing.T) {
	fixture := `COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
juex    43123 user    9u  IPv4  0xabc      0t0  TCP 127.0.0.1:58391 (LISTEN)
juex    43123 user   10u  IPv6  0xdef      0t0  TCP [::1]:58392 (LISTEN)
`
	got := parseLsofTCPListeners(fixture)
	want := []string{"127.0.0.1:58391", "[::1]:58392"}
	if !sameStringSet(got, want) {
		t.Fatalf("listeners = %v, want %v", got, want)
	}
}

func TestProcessTCPListenersReportsUnavailableLsof(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := processTCPListeners(os.Getpid())
	if !errors.Is(err, errProcessListenerScanUnavailable) {
		t.Fatalf("error = %v, want errProcessListenerScanUnavailable", err)
	}
}
