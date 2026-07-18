//go:build windows

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
	output, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: netstat is not installed", errProcessListenerScanUnavailable)
		}
		return nil, fmt.Errorf("scan TCP listeners with netstat: %w", err)
	}
	return parseNetstatTCPListeners(string(output), pid), nil
}

func parseNetstatTCPListeners(output string, pid int) []string {
	pidText := strconv.Itoa(pid)
	var listeners []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 ||
			!strings.EqualFold(fields[0], "TCP") ||
			!strings.EqualFold(fields[3], "LISTENING") ||
			fields[4] != pidText {
			continue
		}
		listeners = append(listeners, fields[1])
	}
	return listeners
}

func TestParseNetstatTCPListeners(t *testing.T) {
	fixture := `
  Proto  Local Address          Foreign Address        State           PID
  TCP    127.0.0.1:58391        0.0.0.0:0              LISTENING       43123
  TCP    127.0.0.1:58392        0.0.0.0:0              LISTENING       99999
  TCP    127.0.0.1:58393        127.0.0.1:443          ESTABLISHED     43123
`
	got := parseNetstatTCPListeners(fixture, 43123)
	want := []string{"127.0.0.1:58391"}
	if !sameStringSet(got, want) {
		t.Fatalf("listeners = %v, want %v", got, want)
	}
}

func TestProcessTCPListenersReportsUnavailableNetstat(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := processTCPListeners(os.Getpid())
	if !errors.Is(err, errProcessListenerScanUnavailable) {
		t.Fatalf("error = %v, want errProcessListenerScanUnavailable", err)
	}
}
