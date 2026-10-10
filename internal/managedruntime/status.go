package managedruntime

import (
	"context"
	"time"
)

type RuntimeStatus struct {
	Initialized     bool             `json:"initialized"`
	MainThreadID    string           `json:"main_thread_id"`
	ActiveThreads   int64            `json:"active_threads"`
	ArchivedThreads int64            `json:"archived_threads"`
	PendingInputs   int64            `json:"pending_inputs"`
	HeldInputs      int64            `json:"held_inputs"`
	States          map[string]int64 `json:"states"`
	LastActivity    *time.Time       `json:"last_activity"`
	ObservedAt      time.Time        `json:"observed_at"`
}

type StatusStore interface {
	Status(context.Context, Scope) (RuntimeStatus, error)
}

func (s *Service) Status(ctx context.Context, actor, tenant, agent string) (RuntimeStatus, error) {
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return RuntimeStatus{}, err
	}
	store, ok := s.Store.(StatusStore)
	if !ok {
		return RuntimeStatus{}, ErrInvalid
	}
	return store.Status(ctx, scope)
}
