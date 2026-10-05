package calendar

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func TestCalendarNoneKeepsContinuousDelayedInstant(t *testing.T) {
	s, scope := NewState(), calendarScope()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := newInterval(t, s, scope, now)
	s.Jobs[id].CatchUp = "none"
	if err := s.Advance(id, now.Add(50*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.Deliveries) != 1 {
		t.Fatal(s.Deliveries)
	}
	for _, d := range s.Deliveries {
		if d.State != "pending" || !d.ScheduledAt.Equal(now.Add(time.Hour)) {
			t.Fatal(d)
		}
	}
	if !s.Jobs[id].NextAt.Equal(now.Add(51 * time.Hour)) {
		t.Fatal(s.Jobs[id])
	}
}

func TestCalendarNoneRecoveryPreservesPreparedAndPostCutoffInstants(t *testing.T) {
	s, scope := NewState(), calendarScope()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := newInterval(t, s, scope, now)
	s.Jobs[id].CatchUp = "none"
	if err := s.Advance(id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var prepared *Delivery
	for _, d := range s.Deliveries {
		prepared = d
	}
	cutoff := now.Add(3*time.Hour + 30*time.Minute)
	if err := s.Recover(id, cutoff); err != nil {
		t.Fatal(err)
	}
	if !s.Jobs[id].NextAt.Equal(now.Add(4 * time.Hour)) {
		t.Fatal(s.Jobs[id])
	}
	if err := s.Advance(id, now.Add(4*time.Hour+30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s.Deliveries[prepared.ID] != prepared || prepared.State != "pending" {
		t.Fatal("prepared delivery changed", prepared)
	}
	pending, skipped := 0, 0
	for _, d := range s.Deliveries {
		if d.State == "pending" {
			pending++
		}
		if d.State == "skipped" {
			skipped++
		}
	}
	if pending != 2 || skipped != 1 {
		t.Fatal(s.Deliveries)
	}
	if err := s.Recover(id, cutoff); err != nil || len(s.Deliveries) != 3 {
		t.Fatal("same recovery repeated", err, s.Deliveries)
	}
}

func TestCalendarMainAndCatchUpValidation(t *testing.T) {
	scope := calendarScope()
	d := Definition{Name: "Wake Main", Content: "Check original context", Mode: "main", AgentID: scope.AgentID, Rule: recurrence.Rule{Frequency: recurrence.FrequencyInterval, EverySeconds: 60}, CatchUp: "none"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.AgentID = ""
	if err := d.Validate(); err == nil {
		t.Fatal("Main without target")
	}
	d.AgentID = scope.AgentID
	d.CatchUp = "sometimes"
	if err := d.Validate(); err == nil {
		t.Fatal("unknown recovery policy")
	}
}

func TestCalendarNoneRecoveryPreservesLunarDatesAndCompletesOnce(t *testing.T) {
	created := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	due := time.Date(2030, 6, 5, 1, 0, 0, 0, time.UTC)
	for _, frequency := range []recurrence.Frequency{recurrence.FrequencyOnce, recurrence.FrequencyYearly} {
		t.Run(string(frequency), func(t *testing.T) {
			rule := recurrence.Rule{Frequency: frequency, Timezone: "Asia/Shanghai", Lunar: &recurrence.LunarOptions{}}
			if frequency == recurrence.FrequencyOnce {
				rule.Year, rule.Month, rule.Day, rule.Time = 2030, 5, 5, "09:00"
			} else {
				rule.Months, rule.Days, rule.Times = []int{5}, []int{5}, []string{"09:00"}
			}
			s, scope := NewState(), calendarScope()
			id := uuid.NewString()
			q := Change{ID: id, Action: "save", Definition: &Definition{Name: "Dragon boat", Content: "Remember today", Mode: "main", AgentID: scope.AgentID, CatchUp: "none", Rule: rule}}
			if _, err := s.Change(scope, scope, "save", q, created); err != nil {
				t.Fatal(err)
			}
			if err := s.Recover(id, due.Add(-time.Hour)); err != nil || !s.Jobs[id].NextAt.Equal(due) || len(s.Deliveries) != 0 {
				t.Fatal("recovery changed future lunar date", err, s.Jobs[id], s.Deliveries)
			}
			cutoff := due.Add(24 * time.Hour)
			if err := s.Recover(id, cutoff); err != nil || len(s.Deliveries) != 1 {
				t.Fatal(err, s.Deliveries)
			}
			for _, d := range s.Deliveries {
				if d.State != "skipped" || !d.ScheduledAt.Equal(due) || !d.Settled {
					t.Fatal(d)
				}
			}
			job := s.Jobs[id]
			if frequency == recurrence.FrequencyOnce {
				if job.Status != "completed" || !job.NextAt.IsZero() {
					t.Fatal(job)
				}
			} else if job.Status != "active" || !job.NextAt.After(cutoff) || job.Rule.Timezone != "Asia/Shanghai" || job.Rule.Lunar == nil {
				t.Fatal(job)
			}
			if err := s.Advance(id, cutoff); err != nil || len(s.Deliveries) != 1 {
				t.Fatal("skipped occurrence was delivered", err, s.Deliveries)
			}
		})
	}
}

func TestCalendarSavedDefaultPolicyReceiptSurvivesUpgrade(t *testing.T) {
	// This is the normalized request persisted before the recovery-policy field
	// existed. Its fingerprint must continue to identify the same default policy.
	encoded := `{"id":"00000000-0000-4000-8000-000000000001","version":0,"action":"save","definition":{"name":"Reminder","content":"Appointment","mode":"reminder","rule":{"frequency":"interval","every_seconds":60},"max_lateness_minutes":1440}}`
	var q Change
	if err := json.Unmarshal([]byte(encoded), &q); err != nil {
		t.Fatal(err)
	}
	scope := calendarScope()
	scope.AgentID = ""
	scope.AgentEpoch = 0
	s := NewState()
	key, err := commandKey(scope, "original-save")
	if err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{ScheduleID: q.ID, Version: 1, State: "active"}
	s.Commands[key] = Command{Scope: scope, Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(encoded))), Receipt: receipt}
	for _, policy := range []string{"", "latest"} {
		q.Definition.CatchUp = policy
		got, err := s.Change(scope, scope, "original-save", q, time.Now())
		if err != nil || got != receipt {
			t.Fatal("same committed request lost receipt", policy, got, err)
		}
	}
	q.Definition.CatchUp = "none"
	if _, err := s.Change(scope, scope, "original-save", q, time.Now()); !errors.Is(err, application.ErrConflict) {
		t.Fatal("new policy reused previous identity", err)
	}
}
