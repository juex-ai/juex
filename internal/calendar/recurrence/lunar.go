package recurrence

import (
	"fmt"
	"slices"
	"sort"
	"time"

	lunar "github.com/6tail/lunar-go/calendar"
)

// Keep conversion inside the dependency's documented approximate year range.
func lunarCandidates(spec Rule, year int) ([]time.Time, error) {
	if year < 1 || year > 9999 {
		return nil, fmt.Errorf("lunar conversion requires a year between 1 and 9999")
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return nil, err
	}
	clocks, err := sortedScheduleClocks("times", spec.Times)
	if err != nil {
		return nil, err
	}
	days := sortedUniqueInts(spec.Days)
	var result []time.Time
	months := lunar.NewLunarYear(year).GetMonthsInYear()
	for item := months.Front(); item != nil; item = item.Next() {
		m := item.Value.(*lunar.LunarMonth)
		month := m.GetMonth()
		if month < 0 {
			month = -month
		}
		if spec.Frequency == FrequencyYearly && !slices.Contains(spec.Months, month) {
			continue
		}
		if (m.IsLeap() && spec.Lunar.LeapMonth == "regular") || (!m.IsLeap() && spec.Lunar.LeapMonth == "leap") {
			continue
		}
		for _, day := range days {
			if day > m.GetDayCount() {
				continue
			}
			solar := lunar.NewLunarFromYmd(year, m.GetMonth(), day).GetSolar()
			for _, clock := range clocks {
				at, ok := exactLocalWallClock(loc, solar.GetYear(), time.Month(solar.GetMonth()), solar.GetDay(), clock)
				if ok && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999 {
					result = append(result, at)
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Before(result[j]) })
	return result, nil
}

func nextLunarOccurrence(spec Rule, now time.Time) (occurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	year := now.In(loc).Year()
	if year < 1 {
		return occurrence{}, false, fmt.Errorf("lunar conversion requires a year between 1 and 9999")
	}
	for y := max(1, year-1); y <= min(9999, year+recurrenceSearchYears); y++ {
		candidates, err := lunarCandidates(spec, y)
		if err != nil {
			return occurrence{}, false, err
		}
		for _, at := range candidates {
			if at.After(now) {
				return occurrenceFor(at), true, nil
			}
		}
	}
	return occurrence{}, false, nil
}

func latestLunarOccurrence(spec Rule, last, now time.Time) (occurrence, bool, error) {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	var latest time.Time
	for y := min(9999, now.In(loc).Year()); y >= max(1, last.In(loc).Year()-1, now.In(loc).Year()-recurrenceSearchYears); y-- {
		candidates, err := lunarCandidates(spec, y)
		if err != nil {
			return occurrence{}, false, err
		}
		for _, at := range candidates {
			if at.After(last) && !at.After(now) && (latest.IsZero() || at.After(latest)) {
				latest = at
			}
		}
		if !latest.IsZero() {
			break
		}
	}
	if latest.IsZero() {
		return occurrence{}, false, nil
	}
	return occurrenceFor(latest), true, nil
}

// A one-shot lunar date resolves only the requested year and month. It never
// searches other years, rolls a missing day forward, or produces two reminders.
func lunarOnceAt(spec Rule) (time.Time, error) {
	month := spec.Month
	if spec.Lunar.LeapMonth == "leap" {
		month = -month
	}
	m := lunar.NewLunarMonthFromYm(spec.Year, month)
	if m == nil || spec.Day > m.GetDayCount() {
		return time.Time{}, fmt.Errorf("lunar date does not exist in year %d: month %d day %d (%s)", spec.Year, spec.Month, spec.Day, spec.Lunar.LeapMonth)
	}
	solar := lunar.NewLunarFromYmd(spec.Year, month, spec.Day).GetSolar()
	if solar.GetYear() < 1 || solar.GetYear() > 9999 {
		return time.Time{}, fmt.Errorf("converted date is outside years 1-9999")
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	clock, err := parseScheduleClock("time", spec.Time)
	if err != nil {
		return time.Time{}, err
	}
	at, ok := exactLocalWallClock(loc, solar.GetYear(), time.Month(solar.GetMonth()), solar.GetDay(), clock)
	if !ok {
		return time.Time{}, fmt.Errorf("lunar once time does not exist in timezone %s", spec.Timezone)
	}
	if at.UTC().Year() < 1 || at.UTC().Year() > 9999 {
		return time.Time{}, fmt.Errorf("converted UTC date is outside years 1-9999")
	}
	return at, nil
}
