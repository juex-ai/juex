package memory

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
)

var ErrWorkerMissing = errors.New("application Worker has not been admitted")

type WorkerState struct{ ID, State string }
type WorkerGateway interface {
	Admit(context.Context, Review) (WorkerState, error)
	State(context.Context, Review) (WorkerState, error)
	Cancel(context.Context, Review) error
}
type JobRepository interface {
	PendingReviews(context.Context, int) ([]Review, error)
}

// Review delivery is retried with the same identity. Runtime freezes admission
// atomically, so competing service instances and lost replies cannot fork work.
func (s *Service) Step(ctx context.Context) error {
	repo, ok := s.Repository.(JobRepository)
	if !ok || s.Workers == nil {
		return application.ErrInvalid
	}
	jobs, err := repo.PendingReviews(ctx, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := s.Repository.Update(call, job.Scope, func(state *State) error {
			current := state.Reviews[job.ID]
			if current == nil {
				return application.ErrDenied
			}
			current.AttemptedAt = time.Now()
			job = *current
			return nil
		})
		if err == nil && !job.WorkerFinished {
			err = s.deliver(call, job)
		}
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

func (s *Service) deliver(ctx context.Context, job Review) error {
	if terminal(job.Receipt.State) && job.DecisionHash == "" {
		if err := s.Workers.Cancel(ctx, job); err != nil {
			return err
		}
		return s.finishWorker(ctx, job, WorkerState{})
	}
	scope, err := s.Authority.AuthorizeApplication(ctx, job.Scope.Access, true)
	if errors.Is(err, application.ErrDenied) || err == nil && !scope.SameAuthority(job.Scope) {
		return s.failWorker(ctx, job, "execution authority changed")
	}
	if err != nil {
		return err
	}
	state, err := s.Workers.State(ctx, job)
	if errors.Is(err, ErrWorkerMissing) {
		if terminal(job.Receipt.State) {
			return s.finishWorker(ctx, job, WorkerState{})
		}
		state, err = s.Workers.Admit(ctx, job)
	}
	if errors.Is(err, application.ErrDenied) || errors.Is(err, application.ErrConflict) || errors.Is(err, application.ErrDisabled) {
		return s.failWorker(ctx, job, "review admission revoked")
	}
	if err != nil {
		return err
	}
	switch state.State {
	case "completed", "failed", "cancelled", "held":
		if !terminal(job.Receipt.State) {
			return s.failWorker(ctx, job, "review Worker ended without a decision: "+state.State)
		}
		return s.finishWorker(ctx, job, state)
	default:
		return s.Repository.Update(ctx, job.Scope, func(stateData *State) error {
			current := stateData.Reviews[job.ID]
			if current == nil {
				return application.ErrDenied
			}
			current.WorkerID = state.ID
			return nil
		})
	}
}

func (s *Service) failWorker(ctx context.Context, job Review, reason string) error {
	err := s.Repository.Update(ctx, job.Scope, func(state *State) error {
		w := state.Reviews[job.ID]
		if w == nil {
			return application.ErrDenied
		}
		if !terminal(w.Receipt.State) {
			w.Receipt.State = "failed"
			w.Receipt.Reason = reason
			w.Receipt.UpdatedAt = time.Now()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.Workers.Cancel(ctx, job); err != nil {
		return err
	}
	return s.finishWorker(ctx, job, WorkerState{})
}
func (s *Service) finishWorker(ctx context.Context, job Review, worker WorkerState) error {
	return s.Repository.Update(ctx, job.Scope, func(state *State) error {
		w := state.Reviews[job.ID]
		if w == nil {
			return application.ErrDenied
		}
		if worker.ID != "" {
			w.WorkerID = worker.ID
		}
		w.WorkerFinished = true
		return nil
	})
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		call, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.Step(call)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("Memory review delivery delayed", "error", err)
		}
	}
}
