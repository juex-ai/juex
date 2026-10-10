package calendar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func calendarImportFixture() (application.Scope, FleetImport) {
	scope := calendarScope()
	owner := scope
	owner.AgentID, owner.AgentEpoch = "", 0
	evaluated := time.Date(2026, 9, 15, 13, 2, 44, 0, time.UTC)
	next := time.Date(2027, 9, 5, 13, 0, 0, 0, time.UTC)
	return owner, FleetImport{Source: "fixture/calendar-v1", SourceSHA256: strings.Repeat("a", 64), CapturedAt: evaluated.Add(time.Hour), Schedules: []ImportedSchedule{{ID: uuid.NewString(), Definition: Definition{Name: "Lunar reminder", Content: "Review the project", Mode: "main", AgentID: scope.AgentID, CatchUp: "none", Rule: recurrence.Rule{Frequency: recurrence.FrequencyYearly, Timezone: "Asia/Shanghai", Months: []int{8}, Days: []int{5}, Times: []string{"21:00"}, Lunar: &recurrence.LunarOptions{LeapMonth: "regular"}}}, Scope: scope, Enabled: true, Clock: recurrence.State{LastEvaluatedAt: evaluated}, NextAt: next}}}
}

func TestCalendarImportPreservesClockWithoutDelivery(t *testing.T) {
	owner, value := calendarImportFixture()
	original, _ := json.Marshal(value)
	state, err := value.BuildState(owner)
	if err != nil {
		t.Fatal(err)
	}
	item := value.Schedules[0]
	job := state.Jobs[item.ID]
	if job == nil || job.Version != 1 || job.Epoch != 1 || job.CatchUp != "none" || job.Mode != "main" || job.Status != "active" || job.NextAt != item.NextAt || job.Clock != item.Clock || job.UpdatedAt != value.CapturedAt || !job.AttemptedAt.IsZero() {
		t.Fatal(job)
	}
	if len(state.Deliveries) != 0 || len(state.Commands) != 0 || state.Control != NewState().Control {
		t.Fatal(state)
	}
	// A recovered source instant is skipped under none; the next continuous
	// occurrence is admitted once with its retained scope and Main mode.
	if err := state.Recover(job.ID, item.NextAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(state.Deliveries) != 1 {
		t.Fatal(state.Deliveries)
	}
	for _, d := range state.Deliveries {
		if d.State != "skipped" || !d.Settled {
			t.Fatal(d)
		}
	}
	future := job.NextAt
	for range 2 {
		if err := state.Advance(job.ID, future); err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Deliveries) != 2 {
		t.Fatal(state.Deliveries)
	}
	var pending int
	for _, d := range state.Deliveries {
		if d.State == "pending" {
			pending++
			if d.Mode != "main" || d.ScheduledAt != future || !d.Scope.SameAuthority(item.Scope) {
				t.Fatal(d)
			}
		}
	}
	if pending != 1 {
		t.Fatal(pending)
	}
	job.Rule.Months[0] = 1
	after, _ := json.Marshal(value)
	if string(original) != string(after) {
		t.Fatal("BuildState aliased or mutated the caller snapshot")
	}
}

func TestCalendarImportRejectsInvalidOwnershipAndClock(t *testing.T) {
	for _, name := range []string{"agent-owner", "foreign", "target", "missing-target", "duplicate", "hash", "capture", "evaluation", "emitted", "next", "disabled-next", "mode", "catch-up"} {
		t.Run(name, func(t *testing.T) {
			owner, value := calendarImportFixture()
			item := &value.Schedules[0]
			switch name {
			case "agent-owner":
				owner.AgentID = item.Scope.AgentID
				owner.AgentEpoch = 1
			case "foreign":
				item.Scope.FleetID = uuid.NewString()
			case "target":
				item.AgentID = uuid.NewString()
			case "missing-target":
				item.Scope.AgentID = ""
			case "duplicate":
				value.Schedules = append(value.Schedules, *item)
			case "hash":
				value.SourceSHA256 = "unverified"
			case "capture":
				value.CapturedAt = time.Time{}
			case "evaluation":
				item.Clock.LastEvaluatedAt = value.CapturedAt.Add(time.Hour)
			case "emitted":
				item.Clock.LastEmittedScheduledAt = item.NextAt
			case "next":
				item.NextAt = item.NextAt.Add(time.Minute)
			case "disabled-next":
				item.Enabled = false
			case "mode":
				item.Mode = "agent"
			case "catch-up":
				item.CatchUp = ""
			}
			if _, err := value.BuildState(owner); err == nil {
				t.Fatal("invalid snapshot admitted", name)
			}
		})
	}
	owner, value := calendarImportFixture()
	value.Schedules[0].Enabled = false
	value.Schedules[0].NextAt = time.Time{}
	state, err := value.BuildState(owner)
	if err != nil || state.Jobs[value.Schedules[0].ID].Status != "paused" {
		t.Fatal(state, err)
	}
}
