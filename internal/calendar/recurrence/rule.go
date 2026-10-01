package recurrence

import (
	"fmt"
	"sync"
	"time"
)

type Frequency string

const (
	FrequencyOnce     Frequency = "once"
	FrequencyDaily    Frequency = "daily"
	FrequencyMonthly  Frequency = "monthly"
	FrequencyYearly   Frequency = "yearly"
	FrequencyInterval Frequency = "interval"
)

type Rule struct {
	Frequency    Frequency     `json:"frequency" jsonschema:"Recurrence; only supply fields applicable to this frequency"`
	Timezone     string        `json:"timezone,omitempty" jsonschema:"IANA timezone required for daily, monthly, yearly and lunar once"`
	At           string        `json:"at,omitempty" jsonschema:"once only: one future RFC3339 timestamp with offset; excludes lunar date fields"`
	Year         int           `json:"year,omitempty" jsonschema:"Lunar once only: one year, 1-9999"`
	Month        int           `json:"month,omitempty" jsonschema:"Lunar once only: one month, 1-12"`
	Day          int           `json:"day,omitempty" jsonschema:"Lunar once only: one day, 1-30"`
	Time         string        `json:"time,omitempty" jsonschema:"Lunar once only: one local HH:MM time"`
	Months       []int         `json:"months,omitempty" jsonschema:"yearly requires months 1-12; all months x days x times combinations fire"`
	Days         []int         `json:"days,omitempty" jsonschema:"monthly and yearly require days: 1-31 Gregorian or 1-30 lunar; absent dates skipped"`
	Times        []string      `json:"times,omitempty" jsonschema:"daily, monthly and yearly require local HH:MM times"`
	Weekdays     []string      `json:"weekdays,omitempty" jsonschema:"daily only: optional mon through sun; omit for every day"`
	EverySeconds int           `json:"every_seconds,omitempty" jsonschema:"interval only: 60-315360000 seconds"`
	Lunar        *LunarOptions `json:"lunar,omitempty" jsonschema:"Include for Chinese lunar once, monthly or yearly; omit for Gregorian"`
}
type LunarOptions struct {
	LeapMonth string `json:"leap_month,omitempty" jsonschema:"regular, leap or both; monthly defaults to both, yearly/once to regular; once forbids both"`
}
type State struct {
	Anchor                 time.Time `json:"anchor"`
	LastEvaluatedAt        time.Time `json:"last_evaluated_at"`
	LastEmittedScheduledAt time.Time `json:"last_emitted_scheduled_at,omitempty"`
}

// lunar-go caches mutable year data, so all conversion shares this lock.
var lunarMu sync.Mutex

func (s *Rule) Validate() error {
	if s.Lunar != nil {
		lunarMu.Lock()
		defer lunarMu.Unlock()
	}
	if len(s.Months) > 12 || len(s.Days) > 31 || len(s.Times) > 48 || len(s.Weekdays) > 7 {
		return fmt.Errorf("recurrence fields exceed their size limits")
	}
	if err := s.validateFrequency(); err != nil {
		return err
	}
	if s.Frequency == FrequencyDaily || s.Frequency == FrequencyMonthly || s.Frequency == FrequencyYearly || s.Lunar != nil {
		if s.Timezone == "" {
			return fmt.Errorf("timezone is required")
		}
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("invalid timezone: %w", err)
		}
	}
	if s.Frequency == FrequencyOnce {
		if s.Lunar != nil {
			at, err := lunarOnceAt(*s)
			if err != nil {
				return err
			}
			s.At = at.UTC().Format(time.RFC3339)
			s.Year, s.Month, s.Day, s.Time, s.Lunar = 0, 0, 0, "", nil
		} else if _, err := parseOnceAt(s.At); err != nil {
			return err
		}
	}

	return nil
}

func (s *Rule) validateFrequency() error {
	// A flat wire format still rejects fields belonging to another frequency.
	fields := []struct {
		name             string
		present, allowed bool
	}{
		{"at", s.At != "", s.Frequency == FrequencyOnce},
		{"year/month/day/time", s.Year != 0 || s.Month != 0 || s.Day != 0 || s.Time != "", s.Frequency == FrequencyOnce && s.Lunar != nil},
		{"months", s.Months != nil, s.Frequency == FrequencyYearly},
		{"days", s.Days != nil, s.Frequency == FrequencyMonthly || s.Frequency == FrequencyYearly},
		{"times", s.Times != nil, s.Frequency == FrequencyDaily || s.Frequency == FrequencyMonthly || s.Frequency == FrequencyYearly},
		{"weekdays", s.Weekdays != nil, s.Frequency == FrequencyDaily},
		{"every_seconds", s.EverySeconds != 0, s.Frequency == FrequencyInterval},
		{"lunar", s.Lunar != nil, s.Frequency == FrequencyOnce || s.Frequency == FrequencyMonthly || s.Frequency == FrequencyYearly},
	}
	for _, field := range fields {
		if field.present && !field.allowed {
			return fmt.Errorf("%s is not allowed for frequency %q", field.name, s.Frequency)
		}
	}
	switch s.Frequency {
	case FrequencyOnce:
		if s.Lunar != nil {
			if s.At != "" {
				return fmt.Errorf("once requires either at or a lunar date, not both")
			}
			if s.Year < 1 || s.Year > 9999 || s.Month < 1 || s.Month > 12 || s.Day < 1 || s.Day > 30 {
				return fmt.Errorf("lunar once requires year 1-9999, month 1-12 and day 1-30")
			}
			if _, err := parseScheduleClock("time", s.Time); err != nil {
				return err
			}
			if err := validateLunarOptions(s.Lunar, "regular"); err != nil {
				return err
			}
			if s.Lunar.LeapMonth == "both" {
				return fmt.Errorf("once requires a single date: lunar.leap_month must be regular or leap")
			}
		}
	case FrequencyDaily:
		if len(s.Times) == 0 {
			return fmt.Errorf("daily requires times")
		}
		if _, err := sortedScheduleClocks("times", s.Times); err != nil {
			return err
		}
		for _, v := range s.Weekdays {
			if _, ok := weekdayNumber(v); !ok {
				return fmt.Errorf("invalid weekday %q", v)
			}
		}
	case FrequencyMonthly, FrequencyYearly:
		if s.Frequency == FrequencyYearly {
			if len(s.Months) == 0 {
				return fmt.Errorf("yearly requires months")
			}
			for _, month := range s.Months {
				if month < 1 || month > 12 {
					return fmt.Errorf("months must be between 1 and 12")
				}
			}
		}
		if len(s.Days) == 0 || len(s.Times) == 0 {
			return fmt.Errorf("%s requires days and times", s.Frequency)
		}
		maxDay := 31
		if s.Lunar != nil {
			maxDay = 30
			defaultLeap := "regular"
			if s.Frequency == FrequencyMonthly {
				defaultLeap = "both"
			}
			if err := validateLunarOptions(s.Lunar, defaultLeap); err != nil {
				return err
			}
		}
		for _, day := range s.Days {
			if day < 1 || day > maxDay {
				return fmt.Errorf("days must be between 1 and %d", maxDay)
			}
		}
		if _, err := sortedScheduleClocks("times", s.Times); err != nil {
			return err
		}
	case FrequencyInterval:
		if s.EverySeconds < 60 || s.EverySeconds > 315360000 {
			return fmt.Errorf("every_seconds must be between 60 and 315360000")
		}
	default:
		return fmt.Errorf("frequency must be once, daily, monthly, yearly or interval")
	}
	return nil
}

func validateLunarOptions(options *LunarOptions, defaultLeap string) error {
	if options.LeapMonth == "" {
		options.LeapMonth = defaultLeap
	}
	switch options.LeapMonth {
	case "regular", "leap", "both":
		return nil
	default:
		return fmt.Errorf("lunar.leap_month must be regular, leap or both")
	}
}
