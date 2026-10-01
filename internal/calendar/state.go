package calendar

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func (d *Definition) Validate() error {
	d.Name, d.Content = strings.TrimSpace(d.Name), strings.TrimSpace(d.Content)
	if d.Name == "" || len(d.Name) > 200 || len([]rune(d.Name)) > 100 || strings.ContainsAny(d.Name, "\r\n") || d.Content == "" || len(d.Content) > 8192 {
		return invalid("name requires 1-200 bytes and content 1-8192 bytes")
	}
	if d.Mode != "agent" && d.Mode != "reminder" {
		return invalid("mode must be agent or reminder")
	}
	if d.Mode == "agent" {
		if _, err := uuid.Parse(d.AgentID); err != nil {
			return invalid("agent mode requires an Agent ID")
		}
	} else if d.AgentID != "" || len(d.Content) > 2048 {
		return invalid("reminder has no target Agent and permits at most 2048 bytes")
	}
	if d.MaxLatenessMinutes == 0 {
		d.MaxLatenessMinutes = 1440
	}
	if d.MaxLatenessMinutes < 1 || d.MaxLatenessMinutes > 1440 {
		return invalid("max_lateness_minutes must be 1-1440")
	}
	if err := d.Rule.Validate(); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func commandKey(scope application.Scope, id string) (string, error) {
	if !scope.Valid() || id == "" || len(id) > 200 {
		return "", application.ErrInvalid
	}
	return scope.ActorID + "/" + scope.AgentID + "/" + id, nil
}

func (s *State) Change(scope, target application.Scope, id string, change Change, now time.Time) (Receipt, error) {
	change, err := normalizeChange(change)
	if err != nil {
		return Receipt{}, err
	}
	key, err := commandKey(scope, id)
	if err != nil {
		return Receipt{}, err
	}
	hash := digest(change)
	if prior, ok := s.Commands[key]; ok {
		if !prior.Scope.SameAuthority(scope) || prior.Cancelled {
			return Receipt{}, application.ErrDenied
		}
		if prior.Fingerprint != hash {
			return Receipt{}, application.ErrConflict
		}
		return prior.Receipt, nil
	}
	if !s.Control.Enabled {
		return Receipt{}, application.ErrDisabled
	}
	receipt, err := s.change(target, change, now.UTC())
	if err != nil {
		return Receipt{}, err
	}
	s.Commands[key] = Command{Scope: scope, Fingerprint: hash, Receipt: receipt}
	return receipt, nil
}

func normalizeChange(q Change) (Change, error) {
	if q.Definition != nil {
		d := *q.Definition
		if d.Rule.Lunar != nil {
			lunar := *d.Rule.Lunar
			d.Rule.Lunar = &lunar
		}
		if err := d.Validate(); err != nil {
			return q, err
		}
		q.Definition = &d
	}
	return q, nil
}

func (s *State) change(target application.Scope, q Change, now time.Time) (Receipt, error) {
	if _, err := uuid.Parse(q.ID); err != nil {
		return Receipt{}, application.ErrInvalid
	}
	job := s.Jobs[q.ID]
	if job == nil {
		if q.Action != "save" || q.Version != 0 || q.Definition == nil {
			return Receipt{}, application.ErrDenied
		}
		if len(s.Jobs) >= 1000 {
			return Receipt{}, application.ErrConflict
		}
		job = &Job{Schedule: Schedule{ID: q.ID, Status: "active"}, Clock: recurrence.State{Anchor: now}}
	} else if q.Version != job.Version {
		return Receipt{}, application.ErrConflict
	}
	if q.Action != "save" && q.Definition != nil || q.Action != "cancel_occurrence" && q.OccurrenceID != "" {
		return Receipt{}, application.ErrInvalid
	}
	switch q.Action {
	case "save":
		if q.Definition == nil || job.Status == "archived" {
			return Receipt{}, application.ErrInvalid
		}
		definition := *q.Definition
		if err := definition.Validate(); err != nil {
			return Receipt{}, err
		}
		if !target.Valid() || target.FleetID == "" || definition.Mode == "agent" && target.AgentID != definition.AgentID {
			return Receipt{}, application.ErrDenied
		}
		if job.Status == "completed" {
			job.Status = "active"
		}
		job.Definition = definition
		job.Scope = target
		// Editing changes future dispatch only. Frozen occurrences keep the old target.
		if job.Status == "active" {
			if err := job.start(now, s.Control.Epoch); err != nil {
				return Receipt{}, err
			}
		}
	case "pause", "archive":
		if job.Status == "archived" {
			return Receipt{}, application.ErrConflict
		}
		job.Status = "paused"
		job.PauseReason = "manual"
		job.NextAt = time.Time{}
		if q.Action == "archive" {
			job.Status = "archived"
		}
	case "restore":
		if job.Status != "archived" {
			return Receipt{}, application.ErrConflict
		}
		job.Status = "paused"
		job.PauseReason = "manual"
	case "resume":
		if job.Status != "paused" || !target.Valid() {
			return Receipt{}, application.ErrConflict
		}
		job.Scope = target
		if err := job.start(now, s.Control.Epoch); err != nil {
			return Receipt{}, err
		}
	case "cancel_occurrence":
		d := s.Deliveries[q.OccurrenceID]
		if d == nil || d.ScheduleID != job.ID {
			return Receipt{}, application.ErrDenied
		}
		if !d.Settled {
			d.CancelRequested = true
			d.AttemptedAt = time.Time{}
			d.Attempt++
			d.UpdatedAt = now
		}
	default:
		return Receipt{}, application.ErrInvalid
	}
	job.Version++
	job.UpdatedAt = now
	s.Jobs[job.ID] = job
	return Receipt{ScheduleID: job.ID, Version: job.Version, State: job.Status, OccurrenceID: q.OccurrenceID}, nil
}

func (j *Job) start(now time.Time, epoch int64) error {
	next, found, err := recurrence.Next(j.Rule, j.Clock, now)
	if err != nil {
		return invalid(err.Error())
	}
	if !found {
		return invalid("schedule has no future occurrence")
	}
	j.Status, j.PauseReason, j.NextAt, j.Epoch = "active", "", next, epoch
	j.Clock.LastEvaluatedAt = now
	return nil
}

func (s *State) Configure(version int64, enabled bool, now time.Time) error {
	if version != s.Control.Version {
		return application.ErrConflict
	}
	if enabled == s.Control.Enabled {
		return nil
	}
	s.Control.Enabled = enabled
	s.Control.Version++
	s.Control.Epoch++
	for _, j := range s.Jobs {
		if j.Status != "active" {
			continue
		}
		j.Version++
		j.UpdatedAt = now
		if enabled {
			if err := j.start(now, s.Control.Epoch); err != nil {
				j.Status = "paused"
				j.PauseReason = "no_future_occurrence"
				j.NextAt = time.Time{}
			}
		} else {
			j.NextAt = time.Time{}
		}
	}
	if !enabled {
		for _, d := range s.Deliveries {
			if !d.Settled {
				d.CancelRequested = true
				d.AttemptedAt = time.Time{}
				d.Attempt++
				d.UpdatedAt = now
			}
		}
	}
	return nil
}

// Advance commits the chosen occurrence and next evaluation boundary together.
// A retry after a crash observes the same ID, never a new execution request.
func (s *State) Advance(id string, now time.Time) error {
	j := s.Jobs[id]
	if j == nil || !s.Control.Enabled || j.Status != "active" || j.NextAt.IsZero() || j.NextAt.After(now) {
		return nil
	}
	at, found, err := recurrence.Latest(j.Rule, j.Clock, now)
	if err != nil {
		return err
	}
	if !found {
		return application.ErrConflict
	}
	if len(s.Deliveries) >= 50000 || s.Status().Pending >= 100 {
		return application.ErrConflict
	}
	occurrenceID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("calendar/"+j.Scope.FleetID+"/"+j.ID+"/"+at.UTC().Format(time.RFC3339Nano))).String()
	if _, exists := s.Deliveries[occurrenceID]; !exists {
		d := &Delivery{Occurrence: Occurrence{ID: occurrenceID, ScheduleID: j.ID, ScheduleVersion: j.Version, Definition: j.Definition, ScheduledAt: at, State: "pending", UpdatedAt: now}, Scope: j.Scope, Epoch: j.Epoch}
		if now.Sub(at) > time.Duration(j.MaxLatenessMinutes)*time.Minute {
			d.State = "missed"
			d.Finished = true
			d.Settled = true
		} else if j.Mode == "reminder" {
			d.State = "completed"
			d.Finished = true
			d.Settled = true
		}
		s.Deliveries[d.ID] = d
	}
	j.Clock.LastEvaluatedAt = now
	j.Clock.LastEmittedScheduledAt = at
	j.UpdatedAt = now
	next, found, err := recurrence.Next(j.Rule, j.Clock, now)
	if err != nil {
		j.Status = "paused"
		j.PauseReason = "no_future_occurrence"
		j.NextAt = time.Time{}
		return nil
	}
	j.NextAt = next
	if !found {
		j.Status = "completed"
		j.NextAt = time.Time{}
	}
	return nil
}

func (s *State) CancelCommand(scope application.Scope, id string) error {
	key, err := commandKey(scope, id)
	if err != nil {
		return err
	}
	if prior, exists := s.Commands[key]; exists {
		if !prior.Scope.SameAuthority(scope) {
			return application.ErrDenied
		}
		return nil
	}
	s.Commands[key] = Command{Scope: scope, Cancelled: true}
	return nil
}
