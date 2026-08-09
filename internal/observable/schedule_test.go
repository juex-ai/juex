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
	domain, err := NewScheduleSpec("queue-check", "", ScheduleSourceSpec{
		Interval:    &IntervalSchedule{EverySeconds: 1800},
		CatchUp:     CatchUpSpec{Mode: ScheduleCatchUpLatest, MaxLatenessMinutes: 60},
		Observation: ScheduleObservationSpec{Kind: "heartbeat", Severity: "info", Content: "check queue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := domain.scheduleRuntime()
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

func TestNextScheduledOccurrenceMonthlyUsesCalendarDaysAndTimezone(t *testing.T) {
	spec := monthlyScheduleSpec("month-end", "Asia/Shanghai", []int{31}, []string{"09:00"})
	now := time.Date(2026, 1, 31, 2, 0, 0, 0, time.UTC)
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 3, 31, 1, 0, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want %s", next.ScheduledAt, want)
	}
	if next.SourceEventID != "schedule:month-end:2026-03-31T01:00:00Z" {
		t.Fatalf("source event id = %q", next.SourceEventID)
	}
}

func TestNextScheduledOccurrenceMonthlySkipsMissingFebruaryDays(t *testing.T) {
	spec := monthlyScheduleSpec("missing-february-days", "UTC", []int{29, 30, 31}, []string{"09:00"})
	now := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2027, 3, 29, 9, 0, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want %s after skipping missing February days 29, 30, and 31", next.ScheduledAt, want)
	}
}

func TestNextScheduledOccurrenceMonthlyHandlesLeapDay(t *testing.T) {
	spec := monthlyScheduleSpec("leap-day", "UTC", []int{29}, []string{"09:00"})
	now := time.Date(2028, 1, 29, 10, 0, 0, 0, time.UTC)
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2028, 2, 29, 9, 0, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want %s", next.ScheduledAt, want)
	}
}

func TestLatestMissedScheduledOccurrenceMonthlyDoesNotRollMissingDay(t *testing.T) {
	spec := monthlyScheduleSpec("month-end", "UTC", []int{31}, []string{"09:00"})
	last := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	latest, ok, err := latestMissedScheduledOccurrence(spec, ScheduleStateRecord{LastEvaluatedAt: last}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("latestMissedScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)
	if !latest.ScheduledAt.Equal(want) {
		t.Fatalf("latest missed = %s, want %s", latest.ScheduledAt, want)
	}
}

func TestLatestMissedScheduledOccurrenceMonthlyFindsCurrentMonthMinute(t *testing.T) {
	scheduledAt := time.Date(2026, 7, 6, 9, 59, 0, 0, time.UTC)
	spec := monthlyScheduleSpec("monthly-catch-up", "UTC", []int{6}, []string{"09:59"})
	latest, ok, err := latestMissedScheduledOccurrence(spec, ScheduleStateRecord{
		LastEvaluatedAt: scheduledAt.Add(-time.Minute),
	}, scheduledAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !latest.ScheduledAt.Equal(scheduledAt) {
		t.Fatalf("latest missed = %+v ok=%v, want %s", latest, ok, scheduledAt)
	}
	if !catchUpAllows(spec, latest, scheduledAt.Add(time.Minute)) {
		t.Fatal("monthly catch-up should allow the one-minute-late occurrence")
	}
}

func TestNextScheduledOccurrenceMonthlySkipsDSTGap(t *testing.T) {
	spec := monthlyScheduleSpec("dst-gap", "America/New_York", []int{8}, []string{"02:30"})
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 4, 8, 6, 30, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want %s", next.ScheduledAt, want)
	}
}

func TestNextScheduledOccurrenceMonthlyUsesEarlierDSTFoldInstant(t *testing.T) {
	spec := monthlyScheduleSpec("dst-fold", "America/New_York", []int{1}, []string{"01:30"})
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	next, ok, err := nextScheduledOccurrence(spec, ScheduleStateRecord{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("nextScheduledOccurrence ok = false, want true")
	}
	want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	if !next.ScheduledAt.Equal(want) {
		t.Fatalf("next scheduled = %s, want earlier fold instant %s", next.ScheduledAt, want)
	}
}

func TestMonthlyScheduleSummarySortsAndDeduplicates(t *testing.T) {
	spec := monthlyScheduleSpec("monthly-summary", "Asia/Shanghai", []int{15, 1, 15}, []string{"17:30", "09:00", "09:00"})
	if got, want := scheduleSummary(spec), "monthly days 1,15 at 09:00,17:30 Asia/Shanghai"; got != want {
		t.Fatalf("schedule summary = %q, want %q", got, want)
	}
}

func scheduleSpec(id string) scheduleRuntimeSpec {
	spec, err := NewScheduleSpec(id, "", ScheduleSourceSpec{
		Timezone: "Asia/Shanghai",
		Daily: &DailySchedule{
			Times:    []string{"09:00"},
			Weekdays: []string{"mon", "tue", "wed", "thu", "fri"},
		},
		CatchUp:     CatchUpSpec{Mode: ScheduleCatchUpLatest, MaxLatenessMinutes: 120},
		Observation: ScheduleObservationSpec{Kind: "heartbeat", Severity: "info", Content: "prepare brief"},
	})
	if err != nil {
		panic(err)
	}
	runtime, _ := spec.scheduleRuntime()
	return runtime
}

func monthlyScheduleSpec(id, timezone string, days []int, times []string) scheduleRuntimeSpec {
	spec, err := NewScheduleSpec(id, "", ScheduleSourceSpec{
		Timezone: timezone,
		Monthly:  &MonthlySchedule{Days: days, Times: times},
		CatchUp:  CatchUpSpec{Mode: ScheduleCatchUpLatest, MaxLatenessMinutes: 120},
		Observation: ScheduleObservationSpec{
			Kind: "heartbeat", Severity: "info", Content: "prepare monthly brief",
		},
	})
	if err != nil {
		panic(err)
	}
	runtime, _ := spec.scheduleRuntime()
	return runtime
}
