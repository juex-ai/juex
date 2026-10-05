package execution

import (
	"context"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
)

func (s *Service) Purge(ctx context.Context, request lifecycle.Request) (lifecycle.Receipt, error) {
	store, ok := s.Store.(lifecycle.Participant)
	if !ok || s.Blobs == nil {
		return lifecycle.Receipt{}, execprotocol.ErrUnavailable
	}
	// Byte writes and the database barrier must be ordered in the same process;
	// the Blob store's exclusive volume ownership also excludes another writer.
	s.Blobs.mu.Lock()
	result, err := store.Purge(ctx, request)
	s.Blobs.mu.Unlock()
	if err != nil || request.Phase != lifecycle.Erase {
		return result, err
	}
	if err = s.Blobs.Reconcile(ctx); err != nil {
		return result, err
	}
	if result.EnvironmentsPending > 0 && s.Managed != nil {
		if err = s.Managed.Reconcile(ctx); err != nil {
			return result, err
		}
	}
	s.Blobs.mu.Lock()
	defer s.Blobs.mu.Unlock()
	return store.Purge(ctx, request)
}
