package observable

import (
	"crypto/rand"
	"encoding/hex"
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

func nextScheduledOccurrence(spec scheduleRuntimeSpec, state ScheduleStateRecord, now time.Time) (ScheduledOccurrence, bool, error) {
	now = normalizeNow(now)
	switch {
	case spec.Once != nil:
		at, err := parseOnceAt(spec.Once.At)
		if err != nil {
			return ScheduledOccurrence{}, false, err
		}
		if state.LastEmittedScheduledAt.Equal(at) || !at.After(now) {
			return ScheduledOccurrence{}, false, nil
		}
		return occurrenceFor(spec, at), true, nil
	case spec.Daily != nil:
		return nextDailyOccurrence(spec, now)
	case spec.Monthly != nil:
		return nextMonthlyOccurrence(spec, now)
	case spec.Interval != nil:
		every := time.Duration(spec.Interval.EverySeconds) * time.Second
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
		return ScheduledOccurrence{}, false, fmt.Errorf("schedule source must set once, daily, monthly, or interval")
	}
}

func latestMissedScheduledOccurrence(spec scheduleRuntimeSpec, state ScheduleStateRecord, now time.Time) (ScheduledOccurrence, bool, error) {
	now = normalizeNow(now)
	if state.LastEvaluatedAt.IsZero() || !state.LastEvaluatedAt.Before(now) {
		return ScheduledOccurrence{}, false, nil
	}
	last := state.LastEvaluatedAt
	switch {
	case spec.Once != nil:
		at, err := parseOnceAt(spec.Once.At)
		if err != nil {
			return ScheduledOccurrence{}, false, err
		}
		if at.After(last) && !at.After(now) && !state.LastEmittedScheduledAt.Equal(at) {
			return occurrenceFor(spec, at), true, nil
		}
		return ScheduledOccurrence{}, false, nil
	case spec.Daily != nil:
		return latestDailyOccurrence(spec, last, now)
	case spec.Monthly != nil:
		return latestMonthlyOccurrence(spec, last, now)
	case spec.Interval != nil:
		every := time.Duration(spec.Interval.EverySeconds) * time.Second
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
		return ScheduledOccurrence{}, false, fmt.Errorf("schedule source must set once, daily, monthly, or interval")
	}
}

func catchUpAllows(spec scheduleRuntimeSpec, occurrence ScheduledOccurrence, now time.Time) bool {
	catchUp := spec.CatchUp
	if catchUp.Mode != ScheduleCatchUpLatest {
		return false
	}
	lateFor := normalizeNow(now).Sub(occurrence.ScheduledAt)
	if lateFor < 0 {
		return false
	}
	return lateFor <= time.Duration(catchUp.MaxLatenessMinutes)*time.Minute
}

func nextDailyOccurrence(spec scheduleRuntimeSpec, now time.Time) (ScheduledOccurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	clocks, err := sortedDailyClocks(spec.Daily.Times)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	start := now.In(loc)
	for day := 0; day <= 366; day++ {
		date := start.AddDate(0, 0, day)
		if !dailyWeekdayAllowed(spec.Daily, date.Weekday()) {
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

func latestDailyOccurrence(spec scheduleRuntimeSpec, last, now time.Time) (ScheduledOccurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	clocks, err := sortedDailyClocks(spec.Daily.Times)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	start := last.In(loc)
	end := now.In(loc)
	var latest time.Time
	for date := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc); !date.After(end); date = date.AddDate(0, 0, 1) {
		if !dailyWeekdayAllowed(spec.Daily, date.Weekday()) {
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

func nextMonthlyOccurrence(spec scheduleRuntimeSpec, now time.Time) (ScheduledOccurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	start := now.In(loc)
	for monthOffset := 0; monthOffset <= 24; monthOffset++ {
		year, month := shiftedMonth(start.Year(), start.Month(), monthOffset)
		for _, candidate := range monthlyCandidates(spec.Monthly, loc, year, month) {
			if candidate.After(now) {
				return occurrenceFor(spec, candidate), true, nil
			}
		}
	}
	return ScheduledOccurrence{}, false, nil
}

func latestMonthlyOccurrence(spec scheduleRuntimeSpec, last, now time.Time) (ScheduledOccurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return ScheduledOccurrence{}, false, err
	}
	start := last.In(loc)
	end := now.In(loc)
	monthCount := (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
	var latest time.Time
	for monthOffset := 0; monthOffset <= monthCount; monthOffset++ {
		year, month := shiftedMonth(start.Year(), start.Month(), monthOffset)
		for _, candidate := range monthlyCandidates(spec.Monthly, loc, year, month) {
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

func monthlyCandidates(spec *MonthlySchedule, loc *time.Location, year int, month time.Month) []time.Time {
	if spec == nil {
		return nil
	}
	days := sortedUniqueMonthlyDays(spec.Days)
	clocks, err := sortedScheduleClocks("schedule_config.monthly.times", spec.Times)
	if err != nil {
		return nil
	}
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	candidates := make([]time.Time, 0, len(days)*len(clocks))
	for _, day := range days {
		if day > lastDay {
			continue
		}
		for _, clock := range clocks {
			candidate, ok := exactLocalWallClock(loc, year, month, day, clock)
			if ok {
				candidates = append(candidates, candidate)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates
}

func shiftedMonth(year int, month time.Month, offset int) (int, time.Month) {
	shifted := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, offset, 0)
	return shifted.Year(), shifted.Month()
}

func exactLocalWallClock(loc *time.Location, year int, month time.Month, day int, clock dailyClock) (time.Time, bool) {
	wallUTC := time.Date(year, month, day, clock.hour, clock.minute, 0, 0, time.UTC)
	offsets := make(map[int]struct{})
	for hour := -36; hour <= 36; hour++ {
		_, offset := wallUTC.Add(time.Duration(hour) * time.Hour).In(loc).Zone()
		offsets[offset] = struct{}{}
	}
	var candidates []time.Time
	for offset := range offsets {
		candidate := wallUTC.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		if local.Year() == year && local.Month() == month && local.Day() == day &&
			local.Hour() == clock.hour && local.Minute() == clock.minute {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return time.Time{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates[0], true
}

func occurrenceFor(spec scheduleRuntimeSpec, scheduledAt time.Time) ScheduledOccurrence {
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

func scheduleManualSourceEventID(observableID string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("schedule manual source event id: %w", err)
	}
	return scheduleSourceEventPrefix(observableID) + "manual:" + hex.EncodeToString(suffix[:]), nil
}

func scheduleSummary(spec scheduleRuntimeSpec) string {
	switch {
	case spec.Once != nil:
		return "once " + strings.TrimSpace(spec.Once.At)
	case spec.Daily != nil:
		weekdays := "every day"
		if len(spec.Daily.Weekdays) > 0 {
			weekdays = strings.Join(spec.Daily.Weekdays, ",")
		}
		return fmt.Sprintf("daily %s %s %s", strings.Join(spec.Daily.Times, ","), weekdays, spec.Timezone)
	case spec.Monthly != nil:
		days := sortedUniqueMonthlyDays(spec.Monthly.Days)
		dayValues := make([]string, 0, len(days))
		for _, day := range days {
			dayValues = append(dayValues, strconv.Itoa(day))
		}
		clocks, _ := sortedScheduleClocks("schedule_config.monthly.times", spec.Monthly.Times)
		clockValues := make([]string, 0, len(clocks))
		for _, clock := range clocks {
			clockValues = append(clockValues, fmt.Sprintf("%02d:%02d", clock.hour, clock.minute))
		}
		return fmt.Sprintf("monthly days %s at %s %s", strings.Join(dayValues, ","), strings.Join(clockValues, ","), spec.Timezone)
	case spec.Interval != nil:
		return fmt.Sprintf("every %ds", spec.Interval.EverySeconds)
	default:
		return ""
	}
}

type dailyClock struct {
	hour   int
	minute int
}

func sortedDailyClocks(values []string) ([]dailyClock, error) {
	return sortedScheduleClocks("source.daily.times", values)
}

func parseDailyClock(value string) (dailyClock, error) {
	return parseScheduleClock("source.daily.times", value)
}

func sortedScheduleClocks(field string, values []string) ([]dailyClock, error) {
	unique := make(map[dailyClock]struct{}, len(values))
	for _, value := range values {
		clock, err := parseScheduleClock(field, value)
		if err != nil {
			return nil, err
		}
		unique[clock] = struct{}{}
	}
	out := make([]dailyClock, 0, len(unique))
	for clock := range unique {
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

func parseScheduleClock(field, value string) (dailyClock, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return dailyClock{}, fmt.Errorf("%s must use HH:MM, got %q", field, value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return dailyClock{}, fmt.Errorf("%s must use HH:MM, got %q", field, value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return dailyClock{}, fmt.Errorf("%s must use HH:MM, got %q", field, value)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return dailyClock{}, fmt.Errorf("%s must use HH:MM, got %q", field, value)
	}
	return dailyClock{hour: hour, minute: minute}, nil
}

func sortedUniqueMonthlyDays(values []int) []int {
	unique := make(map[int]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	out := make([]int, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
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
