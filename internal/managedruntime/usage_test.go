package managedruntime

import (
	"testing"
	"time"
)

func TestUsageDefaultRangeFollowsDeploymentDateAcrossDayBoundary(t *testing.T) {
	q := UsageQuery{Group: "day", Limit: 100}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for zone, want := range map[string]string{"Asia/Shanghai": "2026-10-02", "Pacific/Kiritimati": "2026-10-03", "America/Adak": "2026-10-02"} {
		got, err := q.WithDefaultRange(now, zone)
		if err != nil || got.Until != want || got.Validate(true) != nil {
			t.Fatal(zone, got, err)
		}
	}
	q.From, q.Until = "2025-01-01", "2025-02-01"
	got, err := q.WithDefaultRange(now, "Pacific/Kiritimati")
	if err != nil || got != q {
		t.Fatal("explicit dates changed with deployment timezone", got, err)
	}
}
