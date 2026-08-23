package processidentity

import (
	"os"
	"testing"
	"time"
)

func TestStartedAtCurrentProcessIsStable(t *testing.T) {
	first, err := StartedAt(os.Getpid())
	if err != nil {
		t.Skipf("process start time unavailable: %v", err)
	}
	second, err := StartedAt(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first.IsZero() || !first.Equal(second) {
		t.Fatalf("process start times = %v and %v, want one stable non-zero identity", first, second)
	}
	now := time.Now().UTC()
	if first.After(now.Add(time.Second)) || first.Before(now.Add(-24*time.Hour)) {
		t.Fatalf("process start time = %v, want within the last 24 hours", first)
	}
}

func TestFingerprintCurrentProcessIsStable(t *testing.T) {
	first, err := Fingerprint(os.Getpid())
	if err != nil {
		t.Skipf("process identity unavailable: %v", err)
	}
	second, err := Fingerprint(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("process fingerprints = %q and %q, want one stable non-empty identity", first, second)
	}
}
