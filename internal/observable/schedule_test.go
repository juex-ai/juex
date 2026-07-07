package observable

import (
	"testing"
	"time"
)

func TestNextScheduledOccurrenceDailyUsesTimezoneAndWeekdays(t *testing.T) {
	spec := scheduleSpec("weekday-brief")
	now := time.Date(2026, 7, 3, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{LastEvaluatedAt: now.Add(-time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want %s", next.ScheduledAt, want)
	}
}

func TestNextScheduledOccurrenceDailySkipsWeekend(t *testing.T) {
	spec := scheduleSpec("weekday-brief")
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 7, 6, 1, 0, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want %s", next.ScheduledAt, want)
	}
}

func TestLatestMissedScheduledOccurrenceUsesLatestWithinWindow(t *testing.T) {
	spec := scheduleSpec("weekday-brief")
	last := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 3, 2, 0, 0, 0, time.UTC)
	latest, ok, err := latestMissedScheduledOccurrence(spec, ScheduleStateRecord{LastEvaluatedAt: last}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("latestMissedScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)
	if !latest.ScheduledAt.Equal(want) {
		t.Fatalf("latest missed = %s, want %s", latest.ScheduledAt, want)
	}
	if !catchUpAllows(spec, latest, now) {
		t.Fatal("catchUpAllows = false, want true")
	}
}

func TestLatestMissedScheduledOccurrenceInterval(t *testing.T) {
	spec := Spec{
		ID: "queue-check",
		Source: SourceSpec{
			Type:     SourceTypeSchedule,
			Interval: &IntervalSchedule{EverySeconds: 1800},
			CatchUp:  CatchUpSpec{Mode: ScheduleCatchUpLatest, MaxLatenessMinutes: 60},
		},
		Observation: ObservationSpec{Kind: "heartbeat", Severity: "info", Content: "check queue"},
	}
	last := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	now := last.Add(91 * time.Minute)
	latest, ok, err := latestMissedScheduledOccurrence(spec, ScheduleStateRecord{LastEvaluatedAt: last}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("latestMissedScheduledOccurrence ok = false, want true")
	}
	want := last.Add(90 * time.Minute)
	if !latest.ScheduledAt.Equal(want) {
		t.Fatalf("latest missed = %s, want %s", latest.ScheduledAt, want)
	}
}

func scheduleSpec(id string) Spec {
	return Spec{
		ID: id,
		Source: SourceSpec{
			Type:     SourceTypeSchedule,
			Timezone: "Asia/Shanghai",
			Daily: &DailySchedule{
				Times:    []string{"09:00"},
				Weekdays: []string{"mon", "tue", "wed", "thu", "fri"},
			},
			CatchUp: CatchUpSpec{Mode: ScheduleCatchUpLatest, MaxLatenessMinutes: 120},
		},
		Observation: ObservationSpec{Kind: "heartbeat", Severity: "info", Content: "prepare brief"},
	}
}
