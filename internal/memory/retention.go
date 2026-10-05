package memory

import (
	"context"
	"github.com/juex-ai/juex/internal/foundation/application"
	"time"
)

type EvidenceRetentionRepository interface {
	ExpiredReviewEvidence(context.Context, time.Time, int) ([]application.Scope, error)
}

func (s *State) PruneEvidence(before time.Time) {
	for _, review := range s.Reviews {
		if review.Imported == nil && review.WorkerFinished && terminal(review.Receipt.State) && review.Receipt.UpdatedAt.Before(before) {
			review.Proposal.Text, review.Proposal.Reason = "", ""
			review.Proposal.Evidence, review.Proposal.Sources = nil, nil
		}
	}
}
func (s *Service) pruneEvidence(ctx context.Context) error {
	repo, ok := s.Repository.(EvidenceRetentionRepository)
	if !ok {
		return nil
	}
	before := time.Now().Add(-7 * 24 * time.Hour)
	scopes, err := repo.ExpiredReviewEvidence(ctx, before, 20)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if err := s.Repository.Update(ctx, scope, func(state *State) error { state.PruneEvidence(before); return nil }); err != nil {
			return err
		}
	}
	return nil
}
