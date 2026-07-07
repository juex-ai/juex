package observable

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ScheduledOccurrence struct {
	ObservableID    string
	ScheduledAt     time.Time
	SourceEventID   string
	ScheduleSummary string
}

func nextScheduledOccurrence(spec Spec, state ScheduleStateRecord, now time.Time) (ScheduledOccurrence, bool, error) {
	now = normalizeNow(now)
	switch {
	case spec.Source.Once != nil:
		at, err := parseOnceAt(spec.Source.Once.At)
		if err != nil {
			return ScheduledOccurrence{}, false, err
		}
		if state.LastEmittedScheduledAt.Equal(at) || !at.After(now) {
			return ScheduledOccurrence{}, false, nil
		}
		return occurrenceFor(spec, at), true, nil
	case spec.Source.Daily != nil:
		return nextDailyOccurrence(spec, now)
	case spec.Source.Interval != nil:
		every := time.Duration(spec.Source.Interval.EverySeconds) * time.Second
		anchor := state.LastEmittedScheduledAt
		if anchor.IsZero() {
			anchor = state.LastEvaluatedAt
		}
		if anchor.IsZero() {
			anchor = now
		}
		next := anchor.Add(every)
		for !next.After(now) {
			next = next.Add(every)
		}
		return occurrenceFor(spec, next), true, nil
	default:
		return ScheduledOccurrence{}, false, fmt.Errorf("schedule source must set once, daily, or interval")
	}
}

func latestMissedScheduledOccurrence(spec Spec, state ScheduleStateRecord, now time.Time) (ScheduledOccurrence, bool, error) {
	now = normalizeNow(now)
	if state.LastEvaluatedAt.IsZero() || !state.LastEvaluatedAt.Before(now) {
		return ScheduledOccurrence{}, false, nil
	}
	last := state.LastEvaluatedAt
	switch {
	case spec.Source.Once != nil:
		at, err := parseOnceAt(spec.Source.Once.At)
		if err != nil {
			return ScheduledOccurrence{}, false, err
		}
		if at.After(last) && !at.After(now) && !state.LastEmittedScheduledAt.Equal(at) {
			return occurrenceFor(spec, at), true, nil
		}
		return ScheduledOccurrence{}, false, nil
	case spec.Source.Daily != nil:
		return latestDailyOccurrence(spec, last, now)
	case spec.Source.Interval != nil:
		every := time.Duration(spec.Source.Interval.EverySeconds) * time.Second
		anchor := state.LastEmittedScheduledAt
		if anchor.IsZero() {
			anchor = state.LastEvaluatedAt
		}
		next := anchor.Add(every)
		if next.After(now) {
			return ScheduledOccurrence{}, false, nil
		}
		steps := int(now.Sub(next) / every)
		latest := next.Add(time.Duration(steps) * every)
		if !latest.After(last) {
			return ScheduledOccurrence{}, false, nil
		}
		return occurrenceFor(spec, latest), true, nil
	default:
		return ScheduledOccurrence{}, false, fmt.Errorf("schedule source must set once, daily, or interval")
	}
}

func catchUpAllows(spec Spec, occurrence ScheduledOccurrence, now time.Time) bool {
	catchUp := spec.Source.CatchUp
	if catchUp.Mode != ScheduleCatchUpLatest {
		return false
	}
	lateFor := normalizeNow(now).Sub(occurrence.ScheduledAt)
	if lateFor < 0 {
		return false
	}
	return lateFor <= time.Duration(catchUp.MaxLatenessMinutes)*time.Minute
}

func nextDailyOccurrence(spec Spec, now time.Time) (ScheduledOccurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Source.Timezone)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	clocks, err := sortedDailyClocks(spec.Source.Daily.Times)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	start := now.In(loc)
	for day := 0; day <= 366; day++ {
		date := start.AddDate(0, 0, day)
		if !dailyWeekdayAllowed(spec.Source.Daily, date.Weekday()) {
			continue
		}
		for _, clock := range clocks {
			candidate := time.Date(date.Year(), date.Month(), date.Day(), clock.hour, clock.minute, 0, 0, loc)
			if candidate.After(now) {
				return occurrenceFor(spec, candidate), true, nil
			}
		}
	}
	return ScheduledOccurrence{}, false, nil
}

func latestDailyOccurrence(spec Spec, last, now time.Time) (ScheduledOccurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Source.Timezone)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	clocks, err := sortedDailyClocks(spec.Source.Daily.Times)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	start := last.In(loc)
	end := now.In(loc)
	var latest time.Time
	for date := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc); !date.After(end); date = date.AddDate(0, 0, 1) {
		if !dailyWeekdayAllowed(spec.Source.Daily, date.Weekday()) {
			continue
		}
		for _, clock := range clocks {
			candidate := time.Date(date.Year(), date.Month(), date.Day(), clock.hour, clock.minute, 0, 0, loc)
			if candidate.After(last) && !candidate.After(now) && (latest.IsZero() || candidate.After(latest)) {
				latest = candidate
			}
		}
	}
	if latest.IsZero() {
		return ScheduledOccurrence{}, false, nil
	}
	return occurrenceFor(spec, latest), true, nil
}

func occurrenceFor(spec Spec, scheduledAt time.Time) ScheduledOccurrence {
	scheduledAt = scheduledAt.UTC()
	return ScheduledOccurrence{
		ObservableID:    spec.ID,
		ScheduledAt:     scheduledAt,
		SourceEventID:   scheduleSourceEventID(spec.ID, scheduledAt),
		ScheduleSummary: scheduleSummary(spec),
	}
}

func scheduleSourceEventPrefix(observableID string) string {
	return fmt.Sprintf("schedule:%s:", observableID)
}

func scheduleSourceEventID(observableID string, scheduledAt time.Time) string {
	return scheduleSourceEventPrefix(observableID) + scheduledAt.UTC().Format(time.RFC3339Nano)
}

func scheduleSummary(spec Spec) string {
	switch {
	case spec.Source.Once != nil:
		return "once " + strings.TrimSpace(spec.Source.Once.At)
	case spec.Source.Daily != nil:
		weekdays := "every day"
		if len(spec.Source.Daily.Weekdays) > 0 {
			weekdays = strings.Join(spec.Source.Daily.Weekdays, ",")
		}
		return fmt.Sprintf("daily %s %s %s", strings.Join(spec.Source.Daily.Times, ","), weekdays, spec.Source.Timezone)
	case spec.Source.Interval != nil:
		return fmt.Sprintf("every %ds", spec.Source.Interval.EverySeconds)
	default:
		return ""
	}
}

type dailyClock struct {
	hour   int
	minute int
}

func sortedDailyClocks(values []string) ([]dailyClock, error) {
	out := make([]dailyClock, 0, len(values))
	for _, value := range values {
		clock, err := parseDailyClock(value)
		if err != nil {
			return nil, err
		}
		out = append(out, clock)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].hour == out[j].hour {
			return out[i].minute < out[j].minute
		}
		return out[i].hour < out[j].hour
	})
	return out, nil
}

func parseDailyClock(value string) (dailyClock, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return dailyClock{}, fmt.Errorf("source.daily.times must use HH:MM, got %q", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return dailyClock{}, fmt.Errorf("source.daily.times must use HH:MM, got %q", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return dailyClock{}, fmt.Errorf("source.daily.times must use HH:MM, got %q", value)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return dailyClock{}, fmt.Errorf("source.daily.times must use HH:MM, got %q", value)
	}
	return dailyClock{hour: hour, minute: minute}, nil
}

func parseOnceAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("source.once.at is required")
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("source.once.at must be RFC3339 with timezone: %w", err)
	}
	return at.UTC(), nil
}

func dailyWeekdayAllowed(spec *DailySchedule, weekday time.Weekday) bool {
	if spec == nil || len(spec.Weekdays) == 0 {
		return true
	}
	for _, value := range spec.Weekdays {
		if got, ok := weekdayNumber(value); ok && got == weekday {
			return true
		}
	}
	return false
}

func weekdayNumber(value string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sun":
		return time.Sunday, true
	case "mon":
		return time.Monday, true
	case "tue":
		return time.Tuesday, true
	case "wed":
		return time.Wednesday, true
	case "thu":
		return time.Thursday, true
	case "fri":
		return time.Friday, true
	case "sat":
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func normalizeNow(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC()
}
