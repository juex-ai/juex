package calendar

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func calendarScope() application.Scope {
	return application.Scope{Access: application.Access{ActorID: uuid.NewString(), UserID: uuid.NewString(), TenantID: uuid.NewString(), AgentID: uuid.NewString()}, FleetID: uuid.NewString(), ActorEpoch: 1, MemberEpoch: 1, AgentEpoch: 1}
}

func newInterval(t *testing.T, s *State, scope application.Scope, now time.Time) string {
	t.Helper()
	id := uuid.NewString()
	q := Change{ID: id, Action: "save", Definition: &Definition{Name: "Hourly task", Content: "Check the project", Mode: "agent", AgentID: scope.AgentID, Rule: recurrence.Rule{Frequency: recurrence.FrequencyInterval, EverySeconds: 3600}}}
	if _, err := s.Change(scope, scope, uuid.NewString(), q, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCalendarCatchupIsLatestAndOccurrenceIdentitySurvivesEdits(t *testing.T) {
	s := NewState()
	scope := calendarScope()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := newInterval(t, s, scope, now)
	if err := s.Advance(id, now.Add(10*time.Hour+30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(s.Deliveries) != 1 {
		t.Fatal(s.Deliveries)
	}
	var original *Delivery
	for _, d := range s.Deliveries {
		original = d
	}
	if !original.ScheduledAt.Equal(now.Add(10*time.Hour)) || original.State != "pending" {
		t.Fatal(original)
	}
	if err := s.Advance(id, now.Add(10*time.Hour+31*time.Minute)); err != nil || len(s.Deliveries) != 1 {
		t.Fatal(err, s.Deliveries)
	}
	newTarget := scope
	newTarget.AgentID = uuid.NewString()
	definition := s.Jobs[id].Definition
	definition.AgentID = newTarget.AgentID
	if _, err := s.Change(scope, newTarget, "retarget", Change{ID: id, Version: 1, Action: "save", Definition: &definition}, now.Add(10*time.Hour+32*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if original.AgentID != scope.AgentID || original.Scope.AgentID != scope.AgentID || original.ScheduleVersion != 1 {
		t.Fatal("retarget changed original occurrence", original)
	}
	if err := s.Advance(id, now.Add(11*time.Hour)); err != nil || len(s.Deliveries) != 2 {
		t.Fatal(err, s.Deliveries)
	}
	for _, d := range s.Deliveries {
		if d.ID != original.ID && (d.AgentID != newTarget.AgentID || d.ScheduleVersion != 2) {
			t.Fatal(d)
		}
	}
}

func TestCalendarPauseResumeAndAppDisableDoNotBackfill(t *testing.T) {
	s := NewState()
	scope := calendarScope()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := newInterval(t, s, scope, now)
	if _, err := s.Change(scope, scope, "pause", Change{ID: id, Version: 1, Action: "pause"}, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(id, now.Add(5*time.Hour)); err != nil || len(s.Deliveries) != 0 {
		t.Fatal(err)
	}
	if _, err := s.Change(scope, scope, "resume", Change{ID: id, Version: 2, Action: "resume"}, now.Add(5*time.Hour+12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !s.Jobs[id].NextAt.Equal(now.Add(6 * time.Hour)) {
		t.Fatal("interval anchor shifted", s.Jobs[id])
	}
	if err := s.Advance(id, now.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(1, false, now.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, d := range s.Deliveries {
		if !d.CancelRequested {
			t.Fatal("disable did not revoke admitted work")
		}
	}
	if err := s.Configure(2, true, now.Add(10*time.Hour+10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !s.Jobs[id].NextAt.Equal(now.Add(11*time.Hour)) || len(s.Deliveries) != 1 {
		t.Fatal(s.Jobs[id], s.Deliveries)
	}
}

func TestCalendarExpiredOnceIsVisibleAndReminderNeedsNoWorker(t *testing.T) {
	for _, late := range []time.Duration{time.Minute, 25 * time.Hour} {
		t.Run(late.String(), func(t *testing.T) {
			s := NewState()
			scope := calendarScope()
			scope.AgentID = ""
			scope.AgentEpoch = 0
			now := time.Now().UTC().Truncate(time.Second)
			id := uuid.NewString()
			q := Change{ID: id, Action: "save", Definition: &Definition{Name: "Reminder", Content: "Appointment", Mode: "reminder", Rule: recurrence.Rule{Frequency: recurrence.FrequencyOnce, At: now.Add(time.Hour).Format(time.RFC3339)}}}
			if _, err := s.Change(scope, scope, "create", q, now); err != nil {
				t.Fatal(err)
			}
			if err := s.Advance(id, now.Add(time.Hour+late)); err != nil {
				t.Fatal(err)
			}
			s.StageNotifications()
			for _, d := range s.Deliveries {
				if !d.Finished || d.Notices["result"] == nil || !d.Notices["result"].MainDone || !d.Notices["result"].Event.Valid() {
					t.Fatal(d)
				}
				if late > 24*time.Hour && (d.State != "missed" || d.Notices["result"].Event.Kind != "attention") {
					t.Fatal(d)
				}
				if late < 24*time.Hour && d.Notices["result"].Event.Kind != "reminder" {
					t.Fatal(d)
				}
			}
			if s.Jobs[id].Status != "completed" {
				t.Fatal(s.Jobs[id])
			}
		})
	}
}

func TestCalendarMutationIdempotencyAndCancelBeforeAcceptance(t *testing.T) {
	s := NewState()
	scope := calendarScope()
	now := time.Now()
	id := newInterval(t, s, scope, now)
	q := Change{ID: id, Version: 1, Action: "pause"}
	first, err := s.Change(scope, scope, "pause", q, now)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.Change(scope, scope, "pause", q, now); err != nil || again != first {
		t.Fatal(again, err)
	}
	q.Action = "archive"
	if _, err := s.Change(scope, scope, "pause", q, now); !errors.Is(err, application.ErrConflict) {
		t.Fatal(err)
	}
	if err := s.CancelCommand(scope, "cancelled-command"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(scope, scope, "cancelled-command", Change{ID: id, Version: 2, Action: "resume"}, now); !errors.Is(err, application.ErrDenied) {
		t.Fatal(err)
	}
}

func TestCalendarNotificationsPreserveResultAndDoNotClaimCancellationSucceeded(t *testing.T) {
	s := NewState()
	scope := calendarScope()
	d := &Delivery{Occurrence: Occurrence{ID: uuid.NewString(), State: "outcome_unknown", UpdatedAt: time.Now()}, Scope: scope, Epoch: 1, Finished: true}
	s.Deliveries[d.ID] = d
	s.StageNotifications()
	unknown := d.Notices["unknown"]
	if unknown == nil || unknown.Event.Kind != "attention" {
		t.Fatal(d)
	}
	d.State = "cancel_requested"
	s.StageNotifications()
	if len(d.Notices) != 1 {
		t.Fatal("cancellation acknowledgment created success notice", d.Notices)
	}
	d.State = "cancelled"
	s.StageNotifications()
	if d.Notices["unknown"] != unknown || d.Notices["result"] == nil || d.Notices["result"].Event.Title != "日程执行已取消" {
		t.Fatal(d.Notices)
	}
}
