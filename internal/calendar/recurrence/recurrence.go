// Package recurrence calculates occurrences without delivery or persistence.
package recurrence

import (
	"fmt"
	"time"
)

// Next returns the first occurrence strictly after after.
func Next(rule Rule, state State, after time.Time) (time.Time, bool, error) {
	rule = cloneRule(rule)
	if err := rule.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if rule.Lunar != nil {
		lunarMu.Lock()
		defer lunarMu.Unlock()
	}
	at, found, err := nextScheduledOccurrence(rule, state, after)
	return at.ScheduledAt, found, err
}

// Latest returns only the latest occurrence in (LastEvaluatedAt, now].
func Latest(rule Rule, state State, now time.Time) (time.Time, bool, error) {
	rule = cloneRule(rule)
	if err := rule.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if rule.Lunar != nil {
		lunarMu.Lock()
		defer lunarMu.Unlock()
	}
	at, found, err := latestMissedScheduledOccurrence(rule, state, now)
	return at.ScheduledAt, found, err
}

func cloneRule(rule Rule) Rule {
	if rule.Lunar != nil {
		lunar := *rule.Lunar
		rule.Lunar = &lunar
	}
	return rule
}

func intervalAt(rule Rule, state State, now time.Time, next bool) (time.Time, error) {
	if state.Anchor.IsZero() {
		return time.Time{}, fmt.Errorf("interval requires a persistent anchor")
	}
	seconds := int64(rule.EverySeconds)
	steps := (now.Unix() - state.Anchor.Unix()) / seconds
	if steps < 0 {
		steps = 0
	}
	at := time.Unix(state.Anchor.Unix()+steps*seconds, int64(state.Anchor.Nanosecond())).UTC()
	if at.After(now) {
		steps--
	}
	if next {
		steps++
	}
	if steps < 1 {
		if !next {
			return time.Time{}, nil
		}
		steps = 1
	}
	return time.Unix(state.Anchor.Unix()+steps*seconds, int64(state.Anchor.Nanosecond())).UTC(), nil
}
