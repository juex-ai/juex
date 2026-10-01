package recurrence

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type occurrence struct {
	ScheduledAt time.Time
}

const recurrenceSearchYears = 10

var errSearchHorizon = errors.New("no matching occurrence in the next 10 years")

func nextScheduledOccurrence(spec Rule, state State, now time.Time) (occurrence, bool, error) {
	now = normalizeNow(now)
	next, found, err := findNextOccurrence(spec, state, now)
	if err != nil || spec.Frequency == FrequencyOnce {
		return next, found, err
	}
	loc := time.UTC
	if spec.Timezone != "" {
		loc, err = time.LoadLocation(spec.Timezone)
		if err != nil {
			return occurrence{}, false, err
		}
	}
	if !found || next.ScheduledAt.After(now.In(loc).AddDate(recurrenceSearchYears, 0, 0)) || next.ScheduledAt.Year() < 1 || next.ScheduledAt.Year() > 9999 {
		return occurrence{}, false, errSearchHorizon
	}
	return next, true, nil
}

func findNextOccurrence(spec Rule, state State, now time.Time) (occurrence, bool, error) {
	now = normalizeNow(now)
	switch spec.Frequency {
	case FrequencyOnce:
		at, err := parseOnceAt(spec.At)
		if err != nil {
			return occurrence{}, false, err
		}
		if state.LastEmittedScheduledAt.Equal(at) || !at.After(now) {
			return occurrence{}, false, nil
		}
		return occurrenceFor(at), true, nil
	case FrequencyDaily:
		return nextDailyOccurrence(spec, now)
	case FrequencyMonthly:
		return nextMonthlyOccurrence(spec, now)
	case FrequencyYearly:
		return nextYearlyOccurrence(spec, now)
	case FrequencyInterval:
		next, err := intervalAt(spec, state, now, true)
		return occurrenceFor(next), err == nil, err
	default:
		return occurrence{}, false, fmt.Errorf("schedule requires a recurrence")
	}
}

func latestMissedScheduledOccurrence(spec Rule, state State, now time.Time) (occurrence, bool, error) {
	now = normalizeNow(now)
	if state.LastEvaluatedAt.IsZero() || !state.LastEvaluatedAt.Before(now) {
		return occurrence{}, false, nil
	}
	last := state.LastEvaluatedAt
	switch spec.Frequency {
	case FrequencyOnce:
		at, err := parseOnceAt(spec.At)
		if err != nil {
			return occurrence{}, false, err
		}
		if at.After(last) && !at.After(now) && !state.LastEmittedScheduledAt.Equal(at) {
			return occurrenceFor(at), true, nil
		}
		return occurrence{}, false, nil
	case FrequencyDaily:
		return latestDailyOccurrence(spec, last, now)
	case FrequencyMonthly:
		return latestMonthlyOccurrence(spec, last, now)
	case FrequencyYearly:
		return latestYearlyOccurrence(spec, last, now)
	case FrequencyInterval:
		latest, err := intervalAt(spec, state, now, false)
		if err != nil || !latest.After(last) {
			return occurrence{}, false, err
		}
		return occurrenceFor(latest), true, nil
	default:
		return occurrence{}, false, fmt.Errorf("schedule requires a recurrence")
	}
}

func nextDailyOccurrence(spec Rule, now time.Time) (occurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	clocks, err := sortedDailyClocks(spec.Times)
	if err != nil {
		return occurrence{}, false, err
	}
	start := now.In(loc)
	for day := 0; day <= 366; day++ {
		date := start.AddDate(0, 0, day)
		if !dailyWeekdayAllowed(spec.Weekdays, date.Weekday()) {
			continue
		}
		for _, clock := range clocks {
			candidate, valid := exactLocalWallClock(loc, date.Year(), date.Month(), date.Day(), clock)
			if !valid {
				continue
			}
			if candidate.After(now) {
				return occurrenceFor(candidate), true, nil
			}
		}
	}
	return occurrence{}, false, nil
}

func latestDailyOccurrence(spec Rule, last, now time.Time) (occurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	clocks, err := sortedDailyClocks(spec.Times)
	if err != nil {
		return occurrence{}, false, err
	}
	start := now.In(loc)
	for day := 0; day <= 366; day++ {
		date := start.AddDate(0, 0, -day)
		if !dailyWeekdayAllowed(spec.Weekdays, date.Weekday()) {
			continue
		}
		for i := len(clocks) - 1; i >= 0; i-- {
			at, valid := exactLocalWallClock(loc, date.Year(), date.Month(), date.Day(), clocks[i])
			if valid && at.After(last) && !at.After(now) {
				return occurrenceFor(at), true, nil
			}
		}
		if date.Before(last.AddDate(0, 0, -1)) {
			break
		}
	}
	return occurrence{}, false, nil
}

func nextMonthlyOccurrence(spec Rule, now time.Time) (occurrence, bool, error) {
	if spec.Lunar != nil {
		return nextLunarOccurrence(spec, now)
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	start := now.In(loc)
	for monthOffset := 0; monthOffset <= 24; monthOffset++ {
		year, month := shiftedMonth(start.Year(), start.Month(), monthOffset)
		for _, candidate := range calendarCandidates(spec.Days, spec.Times, loc, year, month) {
			if candidate.After(now) {
				return occurrenceFor(candidate), true, nil
			}
		}
	}
	return occurrence{}, false, nil
}

func latestMonthlyOccurrence(spec Rule, last, now time.Time) (occurrence, bool, error) {
	if spec.Lunar != nil {
		return latestLunarOccurrence(spec, last, now)
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	start := now.In(loc)
	for offset := 0; offset <= 24; offset++ {
		year, month := shiftedMonth(start.Year(), start.Month(), -offset)
		candidates := calendarCandidates(spec.Days, spec.Times, loc, year, month)
		for i := len(candidates) - 1; i >= 0; i-- {
			at := candidates[i]
			if at.After(last) && !at.After(now) {
				return occurrenceFor(at), true, nil
			}
		}
		if time.Date(year, month+1, 1, 0, 0, 0, 0, loc).Before(last) {
			break
		}
	}
	return occurrence{}, false, nil
}

func calendarCandidates(days []int, times []string, loc *time.Location, year int, month time.Month) []time.Time {
	days = sortedUniqueInts(days)
	clocks, err := sortedScheduleClocks("times", times)
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

func occurrenceFor(at time.Time) occurrence { return occurrence{ScheduledAt: at.UTC()} }

type dailyClock struct {
	hour   int
	minute int
}

func sortedDailyClocks(values []string) ([]dailyClock, error) {
	return sortedScheduleClocks("daily.times", values)
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

func sortedUniqueInts(values []int) []int {
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
		return time.Time{}, fmt.Errorf("once.at is required")
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("once.at must be RFC3339 with timezone: %w", err)
	}
	at = at.UTC()
	if at.Year() < 1 || at.Year() > 9999 {
		return time.Time{}, fmt.Errorf("at must resolve to a UTC year between 1 and 9999")
	}
	return at, nil
}

func dailyWeekdayAllowed(weekdays []string, weekday time.Weekday) bool {
	if len(weekdays) == 0 {
		return true
	}
	for _, value := range weekdays {
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
