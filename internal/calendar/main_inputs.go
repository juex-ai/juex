package calendar

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
)

type MainReceipt struct {
	ThreadID string
	InputID  string
	State    string
}

type MainGateway interface {
	Admit(context.Context, Delivery) (MainReceipt, error)
	State(context.Context, Delivery) (MainReceipt, error)
	Cancel(context.Context, Delivery) (MainReceipt, error)
}

// AssignTrigger exposes only a prepared Main delivery, never a Worker job.
func (s *Service) AssignTrigger(ctx context.Context, scope application.Scope, id string, epoch int64) (Delivery, error) {
	return s.assignment(ctx, scope, id, epoch, "main")
}

func (s *Service) deliverMain(ctx context.Context, d Delivery) error {
	if s.MainInputs == nil {
		return application.ErrInvalid
	}
	var receipt MainReceipt
	var err error
	if d.CancelRequested {
		receipt, err = s.MainInputs.Cancel(ctx, d)
	} else {
		receipt, err = s.MainInputs.State(ctx, d)
		if errors.Is(err, ErrWorkerMissing) {
			receipt, err = s.MainInputs.Admit(ctx, d)
		}
	}
	if err != nil {
		return err
	}
	if receipt.State != "accepted" && receipt.State != "cancelled" {
		return application.ErrInvalid
	}
	if receipt.State == "accepted" && (receipt.ThreadID == "" || receipt.InputID == "") {
		return application.ErrInvalid
	}
	return s.Repository.Update(ctx, d.Scope, func(state *State) error {
		current := state.Deliveries[d.ID]
		if current == nil {
			return application.ErrDenied
		}
		if current.Attempt != d.Attempt {
			return nil
		}
		current.MainThreadID, current.InputID = receipt.ThreadID, receipt.InputID
		current.State = receipt.State
		current.Finished, current.Settled = true, true
		current.CancelRequested = false
		current.UpdatedAt = time.Now()
		return nil
	})
}
