package calendar

import (
	"context"
	"errors"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"log/slog"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

var ErrWorkerMissing = errors.New("calendar occurrence has not been admitted")

type WorkerState struct {
	ID, State  string
	Operations []string
}
type WorkerGateway interface {
	Admit(context.Context, Delivery) (WorkerState, error)
	State(context.Context, Delivery) (WorkerState, error)
	Cancel(context.Context, Delivery) error
}
type PendingRepository interface {
	PendingDeliveries(context.Context, int) ([]Delivery, error)
	PendingNotifications(context.Context, int) ([]Notification, error)
}

func (s *Service) Step(ctx context.Context) error {
	done, err := maintenance.Enter(s.Admission)
	if err != nil {
		return err
	}
	defer done()
	repo, ok := s.Repository.(PendingRepository)
	if !ok {
		return application.ErrInvalid
	}
	var failures []error
	failures = append(failures, s.schedule(ctx))
	deliveries, err := repo.PendingDeliveries(ctx, 100)
	if err != nil {
		failures = append(failures, err)
	}
	for _, d := range deliveries {
		if ctx.Err() != nil {
			break
		}
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := s.deliver(call, d)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	failures = append(failures, s.notify(call))
	cancel()
	return errors.Join(failures...)
}

func (s *Service) advance(ctx context.Context, job Job, scheduler Scheduler) error {
	if err := scheduler.Update(ctx, job.Scope, func(state *State) error {
		j := state.Jobs[job.ID]
		if j == nil {
			return application.ErrDenied
		}
		j.AttemptedAt = time.Now()
		job = *j
		return nil
	}); err != nil {
		return err
	}
	current, err := s.Authority.AuthorizeApplication(ctx, job.Scope.Access, true)
	if err != nil && !errors.Is(err, application.ErrDenied) {
		return err
	}
	allowed := err == nil && current.SameAuthority(job.Scope) && current.Capabilities.Allows(agentpolicy.Calendar)
	return scheduler.Update(ctx, job.Scope, func(state *State) error {
		j := state.Jobs[job.ID]
		if j == nil || j.Version != job.Version || j.Epoch != job.Epoch {
			return nil
		}
		if !allowed {
			j.Status = "paused"
			j.PauseReason = "target_unavailable"
			j.NextAt = time.Time{}
			j.Version++
			j.UpdatedAt = time.Now()
			return nil
		}
		if err := state.Recover(job.ID, scheduler.RecoveredAt()); err != nil {
			return err
		}
		return state.Advance(job.ID, time.Now())
	})
}

func (s *Service) deliver(ctx context.Context, d Delivery) error {
	if err := s.Repository.Update(ctx, d.Scope, func(state *State) error {
		current := state.Deliveries[d.ID]
		if current == nil {
			return application.ErrDenied
		}
		current.AttemptedAt = time.Now()
		current.Attempt++
		d = *current
		return nil
	}); err != nil {
		return err
	}
	if d.Settled {
		return nil
	}
	if !d.CancelRequested {
		var err error
		if !d.Finished {
			_, err = s.assignment(ctx, d.Scope, d.ID, d.Epoch, d.Mode)
		} else {
			current, e := s.Authority.AuthorizeApplication(ctx, d.Scope.Access, true)
			err = e
			if err == nil && (!current.SameAuthority(d.Scope) || !current.Capabilities.Allows(agentpolicy.Calendar)) {
				err = application.ErrDenied
			}
			if err == nil {
				status, e := s.Status(ctx, d.Scope.Access)
				err = e
				if err == nil && (!status.Enabled || status.Epoch != d.Epoch) {
					err = application.ErrDisabled
				}
			}
		}
		if errors.Is(err, application.ErrDenied) || errors.Is(err, application.ErrDisabled) {
			stale := false
			if err := s.Repository.Update(ctx, d.Scope, func(state *State) error {
				current := state.Deliveries[d.ID]
				if current == nil || current.Attempt != d.Attempt || current.Settled {
					stale = true
					return nil
				}
				current.CancelRequested = true
				return nil
			}); err != nil {
				return err
			}
			if stale {
				return nil
			}
			d.CancelRequested = true
		} else if err != nil {
			return err
		}
	}
	if d.Mode == "main" {
		return s.deliverMain(ctx, d)
	}
	if s.Workers == nil {
		return application.ErrInvalid
	}
	if d.CancelRequested {
		if err := s.Workers.Cancel(ctx, d); err != nil {
			return err
		}
	}
	receipt, err := s.Workers.State(ctx, d)
	if errors.Is(err, ErrWorkerMissing) && !d.CancelRequested && !d.Finished {
		receipt, err = s.Workers.Admit(ctx, d)
	}
	if err != nil {
		return err
	}
	return s.Repository.Update(ctx, d.Scope, func(state *State) error {
		current := state.Deliveries[d.ID]
		if current == nil {
			return application.ErrDenied
		}
		if current.Attempt != d.Attempt {
			return nil
		}
		current.WorkerID = receipt.ID
		current.Operations = receipt.Operations
		current.State = receipt.State
		current.UpdatedAt = time.Now()
		current.ExternalPending = len(receipt.Operations) > 0
		switch receipt.State {
		case "completed", "cancelled":
			current.Finished = true
		case "held", "failed":
			current.State = "needs_attention"
			current.Finished = true
		case "outcome_unknown":
			current.Finished = true
		}
		current.Settled = current.Finished && !current.ExternalPending && receipt.State != "outcome_unknown"
		return nil
	})
}

func (s *Service) Run(ctx context.Context) error {
	defer s.CloseScheduler()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			call, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := s.Step(call)
			cancel()
			if err != nil && !errors.Is(err, maintenance.ErrDraining) && ctx.Err() == nil {
				slog.Warn("Calendar scheduler step failed", "error", err)
			}
		}
	}
}
