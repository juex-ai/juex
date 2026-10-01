package recurrence

import (
	"sync"
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCalendarRecurrencesAcrossDSTAndMissingDates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  Rule
		after string
		want  []string
	}{
		{"spring gap", Rule{Frequency: FrequencyDaily, Timezone: "America/New_York", Times: []string{"02:30"}}, "2026-03-07T08:00:00Z", []string{"2026-03-09T06:30:00Z"}},
		{"fall fold once", Rule{Frequency: FrequencyDaily, Timezone: "America/New_York", Times: []string{"01:30"}}, "2026-10-31T08:00:00Z", []string{"2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"}},
		{"absent month day", Rule{Frequency: FrequencyMonthly, Timezone: "Asia/Shanghai", Days: []int{31}, Times: []string{"09:00"}}, "2026-01-31T01:00:00Z", []string{"2026-03-31T01:00:00Z"}},
		{"century leap", Rule{Frequency: FrequencyYearly, Timezone: "UTC", Months: []int{2}, Days: []int{29}, Times: []string{"09:00"}}, "2096-02-29T09:00:00Z", []string{"2104-02-29T09:00:00Z"}},
		// Hong Kong Observatory's 2030 conversion table: lunar fifth month,
		// fifth day is June 5. https://www.hko.gov.hk/tc/gts/time/calendar/text/files/T2030c.txt
		{"lunar dragon boat", Rule{Frequency: FrequencyYearly, Timezone: "Asia/Shanghai", Months: []int{5}, Days: []int{5}, Times: []string{"09:00"}, Lunar: &LunarOptions{}}, "2030-01-01T00:00:00Z", []string{"2030-06-05T01:00:00Z"}},
		{"lunar leap month", Rule{Frequency: FrequencyMonthly, Timezone: "Asia/Shanghai", Days: []int{2, 16}, Times: []string{"09:00"}, Lunar: &LunarOptions{LeapMonth: "leap"}}, "2025-01-01T00:00:00Z", []string{"2025-07-26T01:00:00Z", "2025-08-09T01:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rule.Validate(); err != nil {
				t.Fatal(err)
			}
			start := instant(t, tc.after)
			after := start
			for _, want := range tc.want {
				next, found, err := Next(tc.rule, State{}, after)
				if err != nil || !found || !next.Equal(instant(t, want)) {
					t.Fatal(next, found, err, want)
				}
				latest, found, err := Latest(tc.rule, State{LastEvaluatedAt: start}, next)
				if err != nil || !found || !latest.Equal(next) {
					t.Fatal(latest, found, err)
				}
				after = next
			}
		})
	}
}

func TestCalendarIntervalUsesPersistentAnchor(t *testing.T) {
	rule := Rule{Frequency: FrequencyInterval, EverySeconds: 1800}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	anchor := instant(t, "2026-10-01T00:00:00Z")
	state := State{Anchor: anchor, LastEvaluatedAt: anchor.Add(61 * time.Minute)}
	next, _, err := Next(rule, state, anchor.Add(91*time.Minute))
	if err != nil || !next.Equal(anchor.Add(120*time.Minute)) {
		t.Fatal(next, err)
	}
	latest, _, err := Latest(rule, state, anchor.Add(91*time.Minute))
	if err != nil || !latest.Equal(anchor.Add(90*time.Minute)) {
		t.Fatal(latest, err)
	}
}

func TestCalendarLunarConversionConcurrentAndInvalidDates(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			rule := Rule{Frequency: FrequencyOnce, Timezone: "Asia/Shanghai", Year: 2030, Month: 5, Day: 5, Time: "09:00", Lunar: &LunarOptions{}}
			if err := rule.Validate(); err != nil || rule.At != "2030-06-05T01:00:00Z" {
				t.Error(rule, err)
			}
		})
	}
	wg.Wait()
	invalid := Rule{Frequency: FrequencyOnce, Timezone: "Asia/Shanghai", Year: 2026, Month: 5, Day: 1, Time: "09:00", Lunar: &LunarOptions{LeapMonth: "leap"}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("nonexistent leap month accepted")
	}
}
