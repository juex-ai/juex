package managed

import (
	"context"
	"errors"

	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
)

type MainTriggerRuntime interface {
	AdmitMainTrigger(context.Context, managedruntime.Scope, managedruntime.MainTrigger) (managedruntime.MainTriggerReceipt, error)
	MainTriggerReceipt(context.Context, managedruntime.Scope, string) (managedruntime.MainTriggerReceipt, error)
	CancelMainTrigger(context.Context, managedruntime.Scope, string) (managedruntime.MainTriggerReceipt, error)
}

type CalendarMainInputs struct{ Runtime MainTriggerRuntime }

func calendarTrigger(d calendar.Delivery) managedruntime.MainTrigger {
	return managedruntime.MainTrigger{ID: d.ID, Epoch: d.Epoch, Name: d.Name, Content: d.Content, ScheduledAt: d.ScheduledAt}
}
func mainReceipt(r managedruntime.MainTriggerReceipt, err error) (calendar.MainReceipt, error) {
	return calendar.MainReceipt{ThreadID: r.ThreadID, InputID: r.InputID, State: r.State}, memoryWorkerError(err)
}
func (a CalendarMainInputs) Admit(ctx context.Context, d calendar.Delivery) (calendar.MainReceipt, error) {
	r, err := a.Runtime.AdmitMainTrigger(ctx, workerScope(d.Scope), calendarTrigger(d))
	return mainReceipt(r, err)
}
func (a CalendarMainInputs) State(ctx context.Context, d calendar.Delivery) (calendar.MainReceipt, error) {
	r, err := a.Runtime.MainTriggerReceipt(ctx, workerScope(d.Scope), d.ID)
	if errors.Is(err, managedruntime.ErrDenied) {
		err = calendar.ErrWorkerMissing
	}
	return mainReceipt(r, err)
}
func (a CalendarMainInputs) Cancel(ctx context.Context, d calendar.Delivery) (calendar.MainReceipt, error) {
	r, err := a.Runtime.CancelMainTrigger(ctx, workerScope(d.Scope), d.ID)
	return mainReceipt(r, err)
}

func (a RuntimeApplications) CheckTrigger(ctx context.Context, scope managedruntime.Scope, q managedruntime.MainTrigger) error {
	if a.Calendar == nil {
		return managedruntime.ErrDenied
	}
	gateway, ok := a.Calendar.(interface {
		AssignTrigger(context.Context, application.Scope, string, int64) (calendar.Delivery, error)
	})
	if !ok {
		return managedruntime.ErrDenied
	}
	d, err := gateway.AssignTrigger(ctx, appScope(scope), q.ID, q.Epoch)
	if err != nil {
		return appRuntimeError(err)
	}
	expected := calendarTrigger(d)
	if q.ID != expected.ID || q.Epoch != expected.Epoch || q.Name != expected.Name || q.Content != expected.Content || !q.ScheduledAt.Equal(expected.ScheduledAt) {
		return managedruntime.ErrConflict
	}
	return nil
}
