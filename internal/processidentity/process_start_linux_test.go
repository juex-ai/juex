//go:build linux

package processidentity

import (
	"strings"
	"testing"
	"time"
)

func TestParseLinuxProcessStartedAtHandlesClosingParenInCommand(t *testing.T) {
	stat := []byte("42 (worker) name) S" + strings.Repeat(" 0", 18) + " 250 0 0")
	startTicks, err := parseLinuxProcessStartTicks(42, stat)
	if err != nil {
		t.Fatal(err)
	}
	got, err := linuxProcessStartedAt(startTicks, []byte("cpu 1 2 3\nbtime 1000\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1002, 500_000_000).UTC()
	if !got.Equal(want) {
		t.Fatalf("started at = %v, want %v", got, want)
	}
}

func TestLinuxFingerprintDoesNotDependOnWallClockBootTime(t *testing.T) {
	stat := []byte("42 (worker) S" + strings.Repeat(" 0", 18) + " 250")
	startTicks, err := parseLinuxProcessStartTicks(42, stat)
	if err != nil {
		t.Fatal(err)
	}
	firstStartedAt, err := linuxProcessStartedAt(startTicks, []byte("btime 1000\n"))
	if err != nil {
		t.Fatal(err)
	}
	secondStartedAt, err := linuxProcessStartedAt(startTicks, []byte("btime 2000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if firstStartedAt.Equal(secondStartedAt) {
		t.Fatal("fixture must demonstrate that reconstructed wall-clock start times can differ")
	}
	first := formatLinuxFingerprint("boot-identity", startTicks)
	second := formatLinuxFingerprint("boot-identity", startTicks)
	if first != second {
		t.Fatalf("fingerprints = %q and %q, want stable identity", first, second)
	}
	if strings.Contains(first, "1000") || strings.Contains(first, "2000") {
		t.Fatalf("fingerprint %q unexpectedly embeds wall-clock boot time", first)
	}
}

func TestParseLinuxProcessStartedAtRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name     string
		stat     string
		procStat string
	}{
		{name: "missing command terminator", stat: "42 worker S", procStat: "btime 1000\n"},
		{name: "missing start field", stat: "42 (worker) S 0", procStat: "btime 1000\n"},
		{name: "invalid start field", stat: "42 (worker) S" + strings.Repeat(" 0", 18) + " nope", procStat: "btime 1000\n"},
		{name: "missing boot time", stat: "42 (worker) S" + strings.Repeat(" 0", 18) + " 250", procStat: "cpu 1 2 3\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			startTicks, err := parseLinuxProcessStartTicks(42, []byte(test.stat))
			if err == nil && test.name == "missing boot time" {
				_, err = linuxProcessStartedAt(startTicks, []byte(test.procStat))
			}
			if err == nil {
				t.Fatal("parse succeeded, want error")
			}
		})
	}
}
