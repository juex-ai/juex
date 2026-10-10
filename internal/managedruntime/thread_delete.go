package managedruntime

import (
	"context"
	"github.com/google/uuid"
)

type ThreadDeletionReceipt struct {
	ThreadID string `json:"thread_id"`
	Deleted  bool   `json:"deleted"`
}

type ThreadDeletionStore interface {
	DeleteThread(context.Context, Scope, string) (ThreadDeletionReceipt, error)
}

func (s *Service) DeleteThread(ctx context.Context, actor, tenant, agent, thread string) (ThreadDeletionReceipt, error) {
	if _, err := uuid.Parse(thread); err != nil {
		return ThreadDeletionReceipt{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, true)
	if err != nil {
		return ThreadDeletionReceipt{}, err
	}
	store, ok := s.Store.(ThreadDeletionStore)
	if !ok {
		return ThreadDeletionReceipt{}, ErrInvalid
	}
	return store.DeleteThread(ctx, scope, thread)
}
