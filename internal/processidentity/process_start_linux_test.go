//go:build linux

package processidentity

import (
	"strings"
	"testing"
	"time"
)

func TestParseLinuxProcessStartedAtHandlesClosingParenInCommand(t *testing.T) {
	stat := []byte("42 (worker) name) S" + strings.Repeat(" 0", 18) + " 250 0 0")
	got, err := parseLinuxProcessStartedAt(42, stat, []byte("cpu 1 2 3\nbtime 1000\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1002, 500_000_000).UTC()
	if !got.Equal(want) {
		t.Fatalf("started at = %v, want %v", got, want)
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
			if _, err := parseLinuxProcessStartedAt(42, []byte(test.stat), []byte(test.procStat)); err == nil {
				t.Fatal("parse succeeded, want error")
			}
		})
	}
}
