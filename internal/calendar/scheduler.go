package calendar

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
)

// Scheduler owns a continuous scheduling session. Its writes are fenced by the
// same storage session that establishes RecoveredAt, not by an expiring timer.
type Scheduler interface {
	ActiveSchedules(context.Context, int) ([]Job, error)
	Update(context.Context, application.Scope, func(*State) error) error
	RecoveredAt() time.Time
	Closed() bool
	Close()
}

type SchedulerRepository interface {
	OpenScheduler(context.Context) (Scheduler, error)
}

func (s *Service) schedule(ctx context.Context) error {
	s.schedulerMu.Lock()
	defer s.schedulerMu.Unlock()
	if s.scheduler != nil && s.scheduler.Closed() {
		s.scheduler.Close()
		s.scheduler = nil
	}
	if s.scheduler == nil {
		repo, ok := s.Repository.(SchedulerRepository)
		if !ok {
			return application.ErrInvalid
		}
		var err error
		s.scheduler, err = repo.OpenScheduler(ctx)
		if err != nil {
			return err
		}
		if s.scheduler == nil {
			return nil
		}
	}
	jobs, err := s.scheduler.ActiveSchedules(ctx, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.advance(call, job, s.scheduler)
		cancel()
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (s *Service) CloseScheduler() {
	s.schedulerMu.Lock()
	defer s.schedulerMu.Unlock()
	if s.scheduler != nil {
		s.scheduler.Close()
		s.scheduler = nil
	}
}
