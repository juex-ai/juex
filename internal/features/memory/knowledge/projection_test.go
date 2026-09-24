package knowledge

import (
	"testing"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func TestLifecycleScheduledSupersession(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	for _, tc := range []struct {
		name   string
		status string
		from   *time.Time
		until  *time.Time
		at     time.Time
		asOf   bool
		want   string
	}{
		{"before start", "superseded", &start, &end, start.Add(-time.Millisecond), false, "future"},
		{"at start", "superseded", &start, &end, start, false, "current"},
		{"before end", "superseded", &start, &end, end.Add(-time.Millisecond), false, "current"},
		{"at end", "superseded", &start, &end, end, false, "superseded"},
		{"after end", "superseded", &start, &end, end.Add(time.Millisecond), false, "superseded"},
		{"unknown start current", "superseded", nil, &end, start, false, "current"},
		{"unknown start as of", "superseded", nil, &end, start, true, "uncertain"},
		{"unknown end current", "superseded", &start, nil, start, false, "superseded"},
		{"unknown end as of", "superseded", &start, nil, start, true, "uncertain"},
		{"past truth", "superseded", &start, &end, start, true, "current"},
		{"corrected before end", "corrected", &start, &end, start, false, "corrected"},
		{"retracted before end", "retracted", &start, &end, start, false, "retracted"},
		{"disputed before end", "disputed", &start, &end, start, false, "disputed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := mc.Fact{Status: tc.status, ValidFrom: tc.from, ValidUntil: tc.until}
			if got := Lifecycle(fact, tc.at, tc.asOf); got != tc.want {
				t.Fatalf("Lifecycle() = %q, want %q", got, tc.want)
			}
		})
	}
	// A scheduled end cannot settle an obligation whose deadline already passed.
	fact := mc.Fact{Domain: "obligations", Status: "superseded", ValidFrom: &start, ValidUntil: &end, DueAt: &start}
	if got := Lifecycle(fact, start, false); got != "overdue" {
		t.Fatalf("scheduled obligation = %q, want overdue", got)
	}
}
