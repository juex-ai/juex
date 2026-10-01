package managed

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
)

type RuntimeCalendar interface {
	Status(context.Context, application.Access) (calendar.Status, error)
	Schedules(context.Context, application.Access, int, int) (calendar.SchedulePage, error)
	Occurrences(context.Context, application.Access, string, int, int) (calendar.OccurrencePage, error)
	Change(context.Context, application.Access, *application.Scope, string, calendar.Change) (calendar.Receipt, error)
	Assignment(context.Context, application.Scope, string, int64) (calendar.Delivery, error)
	CancelCommand(context.Context, application.Scope, string) error
}

func (a RuntimeApplications) Tools(ctx context.Context, scope managedruntime.Scope, job *managedruntime.ApplicationJob) (managedruntime.ApplicationTools, error) {
	catalog, err := a.memoryTools(ctx, scope, job)
	if err != nil {
		return catalog, err
	}
	if a.Calendar == nil || job != nil && job.Application == "memory" {
		return catalog, nil
	}
	call, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err = a.Calendar.Status(call, appScope(scope).Access)
	if errors.Is(err, application.ErrDisabled) {
		return catalog, nil
	}
	if errors.Is(err, application.ErrDenied) {
		return catalog, managedruntime.ErrDenied
	}
	// A transient outage does not block unrelated conversation. The actual tool
	// request still checks current authority and application enablement.
	catalog.Tools = append(catalog.Tools, calendar.Tools()...)
	catalog.Instructions += "\n\n" + calendar.AgentGuidance
	return catalog, nil
}

func (a RuntimeApplications) calendarCall(ctx context.Context, work managedruntime.ToolWork) (any, error) {
	if a.Calendar == nil {
		return nil, managedruntime.ErrDenied
	}
	scope := appScope(work.Scope)
	switch work.Call.ToolName {
	case "calendar_schedules":
		var q struct {
			Offset int `json:"offset"`
			Limit  int `json:"limit"`
		}
		if err := decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		return a.Calendar.Schedules(ctx, scope.Access, q.Offset, q.Limit)
	case "calendar_occurrences":
		var q struct {
			ScheduleID string `json:"schedule_id"`
			Offset     int    `json:"offset"`
			Limit      int    `json:"limit"`
		}
		if err := decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		return a.Calendar.Occurrences(ctx, scope.Access, q.ScheduleID, q.Offset, q.Limit)
	case "calendar_change":
		var q calendar.Change
		if err := decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		if q.ID == "" && q.Version == 0 && q.Action == "save" {
			q.ID = work.ID
		}
		return a.Calendar.Change(ctx, scope.Access, &scope, work.ID, q)
	default:
		return nil, managedruntime.ErrInvalid
	}
}
