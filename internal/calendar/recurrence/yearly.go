package recurrence

import "time"

func nextYearlyOccurrence(spec Rule, now time.Time) (occurrence, bool, error) {
	if spec.Lunar != nil {
		return nextLunarOccurrence(spec, now)
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	start := now.In(loc).Year()
	// Search whole years for candidates; the caller applies the exact horizon.
	for year := start; year <= min(9999, start+recurrenceSearchYears); year++ {
		for _, at := range yearlyCandidates(spec, loc, year) {
			if at.After(now) {
				return occurrenceFor(at), true, nil
			}
		}
	}
	return occurrence{}, false, nil
}

func latestYearlyOccurrence(spec Rule, last, now time.Time) (occurrence, bool, error) {
	if spec.Lunar != nil {
		return latestLunarOccurrence(spec, last, now)
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return occurrence{}, false, err
	}
	for year := now.In(loc).Year(); year >= max(last.In(loc).Year(), now.In(loc).Year()-recurrenceSearchYears); year-- {
		candidates := yearlyCandidates(spec, loc, year)
		for i := len(candidates) - 1; i >= 0; i-- {
			at := candidates[i]
			if at.After(last) && !at.After(now) {
				return occurrenceFor(at), true, nil
			}
		}
	}
	return occurrence{}, false, nil
}

func yearlyCandidates(spec Rule, loc *time.Location, year int) []time.Time {
	var result []time.Time
	for _, month := range sortedUniqueInts(spec.Months) {
		result = append(result, calendarCandidates(spec.Days, spec.Times, loc, year, time.Month(month))...)
	}
	return result
}
