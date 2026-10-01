package managed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/managedruntime"
)

type CalendarWorkers struct{ Runtime ApplicationRuntime }

func (a CalendarWorkers) Admit(ctx context.Context, d calendar.Delivery) (calendar.WorkerState, error) {
	job := managedruntime.ApplicationJob{Application: "calendar", ID: d.ID, Epoch: d.Epoch, Name: d.Name, MaxCalls: 32, Instruction: fmt.Sprintf("Execute this scheduled task for the owning user. Scheduled instant: %s. Occurrence ID: %s. Follow existing authorization and report the actual result; do not recreate or replay this occurrence.\n\n%s", d.ScheduledAt.UTC().Format(time.RFC3339), d.ID, d.Content)}
	r, err := a.Runtime.AdmitApplication(ctx, workerScope(d.Scope), job)
	return calendar.WorkerState{ID: r.ThreadID, State: r.State, Operations: r.Operations}, memoryWorkerError(err)
}
func (a CalendarWorkers) State(ctx context.Context, d calendar.Delivery) (calendar.WorkerState, error) {
	r, err := a.Runtime.ApplicationReceipt(ctx, workerScope(d.Scope), "calendar", d.ID)
	if errors.Is(err, managedruntime.ErrDenied) {
		err = calendar.ErrWorkerMissing
	}
	return calendar.WorkerState{ID: r.ThreadID, State: r.State, Operations: r.Operations}, memoryWorkerError(err)
}
func (a CalendarWorkers) Cancel(ctx context.Context, d calendar.Delivery) error {
	return memoryWorkerError(a.Runtime.CancelApplication(ctx, workerScope(d.Scope), "calendar", d.ID))
}
