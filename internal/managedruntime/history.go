package managedruntime

import (
	"context"

	"github.com/google/uuid"
)

// HistoryStore reads bounded windows without initializing or waking an Agent.
type HistoryStore interface {
	History(context.Context, Scope, string, int64, int) (Timeline, error)
}

func (s *Service) History(ctx context.Context, actor, tenant, agent, thread string, before int64, limit int) (Timeline, error) {
	if _, err := uuid.Parse(thread); err != nil || before < 0 || limit < 1 || limit > 500 {
		return Timeline{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return Timeline{}, err
	}
	store, ok := s.Store.(HistoryStore)
	if !ok {
		return Timeline{}, ErrInvalid
	}
	return store.History(ctx, scope, thread, before, limit)
}
