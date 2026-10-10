package managedruntime

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Rune offsets page the immutable original JSON, including retained attachment
// receipts. Inspection never reconnects the source or reads a live device path.
type ObservationContent struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	EnvironmentID   string    `json:"environment_id"`
	OperationID     string    `json:"operation_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Data            string    `json:"data"`
	Offset          int       `json:"offset"`
	NextOffset      int       `json:"next_offset"`
	TotalCharacters int       `json:"total_characters"`
	HasMore         bool      `json:"has_more"`
}

type ObservationContentStore interface {
	ObservationContent(context.Context, Scope, string, int, int) (ObservationContent, error)
}

func (s *Service) ObservationContent(ctx context.Context, actor, tenant, agent, id string, offset, limit int) (ObservationContent, error) {
	if parsed, err := uuid.Parse(id); err != nil || parsed == uuid.Nil || parsed.String() != id || offset < 0 || offset > 1<<30 || limit < 1 || limit > 65536 {
		return ObservationContent{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return ObservationContent{}, err
	}
	store, ok := s.Store.(ObservationContentStore)
	if !ok {
		return ObservationContent{}, ErrInvalid
	}
	return store.ObservationContent(ctx, scope, id, offset, limit)
}
