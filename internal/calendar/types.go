// Package calendar owns Fleet schedules, occurrences and delivery receipts.
package calendar

import (
	"context"
	"time"

	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
)

type Repository interface {
	View(context.Context, application.Scope, func(*State) error) error
	Update(context.Context, application.Scope, func(*State) error) error
}

type Service struct {
	Repository Repository
	Authority  application.Authority
	Workers    WorkerGateway
	Notifier   Notifier
}

type Definition struct {
	Name               string          `json:"name"`
	Content            string          `json:"content"`
	Mode               string          `json:"mode"`
	AgentID            string          `json:"agent_id,omitempty"`
	Rule               recurrence.Rule `json:"rule"`
	MaxLatenessMinutes int             `json:"max_lateness_minutes"`
}

type Schedule struct {
	ID string `json:"id"`
	Definition
	Version     int64     `json:"version"`
	Status      string    `json:"status"`
	PauseReason string    `json:"pause_reason,omitempty"`
	NextAt      time.Time `json:"next_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Job struct {
	Schedule
	Scope       application.Scope `json:"scope"`
	Clock       recurrence.State  `json:"clock"`
	Epoch       int64             `json:"epoch"`
	AttemptedAt time.Time         `json:"attempted_at"`
}

type Occurrence struct {
	ExternalPending bool   `json:"external_pending"`
	CancelRequested bool   `json:"cancel_requested"`
	ID              string `json:"id"`
	ScheduleID      string `json:"schedule_id"`
	ScheduleVersion int64  `json:"schedule_version"`
	Definition
	ScheduledAt time.Time `json:"scheduled_at"`
	State       string    `json:"state"`
	WorkerID    string    `json:"worker_id,omitempty"`
	Operations  []string  `json:"operations,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Delivery struct {
	Occurrence
	Attempt     int64                    `json:"attempt"`
	Scope       application.Scope        `json:"scope"`
	Epoch       int64                    `json:"epoch"`
	Finished    bool                     `json:"finished"`
	AttemptedAt time.Time                `json:"attempted_at"`
	Notices     map[string]*Notification `json:"notices"`
	Settled     bool                     `json:"settled"`
}

type Notification struct {
	Event       application.Event `json:"event"`
	MainDone    bool              `json:"main_done"`
	InboxDone   bool              `json:"inbox_done"`
	AttemptedAt time.Time         `json:"attempted_at"`
}

type Notifier interface {
	Main(context.Context, application.Event) error
	Inbox(context.Context, application.Event) error
}

type Change struct {
	ID           string      `json:"id"`
	Version      int64       `json:"version"`
	Action       string      `json:"action"`
	Definition   *Definition `json:"definition,omitempty"`
	OccurrenceID string      `json:"occurrence_id,omitempty"`
}

type Receipt struct {
	ScheduleID   string `json:"schedule_id"`
	Version      int64  `json:"version"`
	State        string `json:"state"`
	OccurrenceID string `json:"occurrence_id,omitempty"`
}

type Command struct {
	Scope       application.Scope `json:"scope"`
	Fingerprint string            `json:"fingerprint"`
	Cancelled   bool              `json:"cancelled"`
	Receipt     Receipt           `json:"receipt"`
}

type State struct {
	Control    application.Control  `json:"control"`
	Jobs       map[string]*Job      `json:"jobs"`
	Deliveries map[string]*Delivery `json:"deliveries"`
	Commands   map[string]Command   `json:"commands"`
}

func NewState() *State {
	return &State{Control: application.Control{Enabled: true, Epoch: 1, Version: 1}, Jobs: map[string]*Job{}, Deliveries: map[string]*Delivery{}, Commands: map[string]Command{}}
}

type Status struct {
	application.Control
	Schedules int `json:"schedules"`
	Pending   int `json:"pending"`
}

func (s *State) Status() Status {
	status := Status{Control: s.Control, Schedules: len(s.Jobs)}
	for _, d := range s.Deliveries {
		if !d.Settled {
			status.Pending++
		}
	}
	return status
}
