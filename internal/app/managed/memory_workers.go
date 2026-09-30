package managed

import (
	"context"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
)

type ApplicationRuntime interface {
	AdmitApplication(context.Context, managedruntime.Scope, managedruntime.ApplicationJob) (managedruntime.ApplicationReceipt, error)
	ApplicationReceipt(context.Context, managedruntime.Scope, string, string) (managedruntime.ApplicationReceipt, error)
	CancelApplication(context.Context, managedruntime.Scope, string, string) error
}
type MemoryWorkers struct{ Runtime ApplicationRuntime }

func (a MemoryWorkers) Admit(ctx context.Context, review memory.Review) (memory.WorkerState, error) {
	prompt, err := memory.AssignmentPrompt(review)
	if err != nil {
		return memory.WorkerState{}, err
	}
	job := managedruntime.ApplicationJob{Application: "memory", ID: review.ID, Epoch: review.Epoch, Fence: review.Fence, Name: "Memory review", Instruction: prompt, MaxCalls: 24}
	r, err := a.Runtime.AdmitApplication(ctx, workerScope(review.Scope), job)
	return memory.WorkerState{ID: r.ThreadID, State: r.State}, memoryWorkerError(err)
}
func (a MemoryWorkers) State(ctx context.Context, review memory.Review) (memory.WorkerState, error) {
	r, err := a.Runtime.ApplicationReceipt(ctx, workerScope(review.Scope), "memory", review.ID)
	if errors.Is(err, managedruntime.ErrDenied) {
		err = memory.ErrWorkerMissing
	}
	return memory.WorkerState{ID: r.ThreadID, State: r.State}, memoryWorkerError(err)
}
func (a MemoryWorkers) Cancel(ctx context.Context, review memory.Review) error {
	return memoryWorkerError(a.Runtime.CancelApplication(ctx, workerScope(review.Scope), "memory", review.ID))
}
func memoryWorkerError(err error) error {
	switch {
	case errors.Is(err, managedruntime.ErrDenied):
		return application.ErrDenied
	case errors.Is(err, managedruntime.ErrInvalid):
		return application.ErrInvalid
	case errors.Is(err, managedruntime.ErrConflict):
		return application.ErrConflict
	default:
		return err
	}
}
