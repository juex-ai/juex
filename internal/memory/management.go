package memory

import (
	"context"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

// ReviewSummary deliberately excludes private original conversation evidence.
type ReviewSummary struct {
	mc.Receipt
	AgentID  string `json:"agent_id"`
	ThreadID string `json:"thread_id"`
	WorkerID string `json:"worker_id,omitempty"`
}
type ReviewPage struct {
	Reviews []ReviewSummary `json:"reviews"`
	Next    int             `json:"next"`
}
type StorageRules struct {
	Entries []string    `json:"entries"`
	Sources []mc.Source `json:"sources"`
	Next    int         `json:"next"`
}

func (s *Service) Reviews(ctx context.Context, access application.Access, offset, limit int) (ReviewPage, error) {
	value := ReviewPage{Reviews: []ReviewSummary{}}
	if access.AgentID != "" {
		return value, application.ErrDenied
	}
	if offset < 0 || offset > 1<<30 || limit < 1 || limit > 50 {
		return value, application.ErrInvalid
	}
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error {
		var all []ReviewSummary
		for _, review := range state.Reviews {
			all = append(all, ReviewSummary{Receipt: review.Receipt, AgentID: review.Scope.AgentID, ThreadID: review.ThreadID, WorkerID: review.WorkerID})
		}
		slices.SortFunc(all, func(a, b ReviewSummary) int {
			if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		if offset >= len(all) {
			return nil
		}
		end := min(offset+limit, len(all))
		value.Reviews = all[offset:end]
		if end < len(all) {
			value.Next = end
		}
		return nil
	})
	return value, err
}
func (s *Service) StorageRules(ctx context.Context, access application.Access, offset, limit int) (StorageRules, error) {
	value := StorageRules{Entries: []string{}, Sources: []mc.Source{}}
	if offset < 0 || offset > 1<<30 || limit < 1 || limit > 50 {
		return value, application.ErrInvalid
	}
	if access.AgentID != "" {
		return value, application.ErrDenied
	}
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error {
		for id := range state.Deleted {
			value.Entries = append(value.Entries, id)
		}
		slices.Sort(value.Entries)
		total := max(len(value.Entries), len(state.Suppressed))
		value.Entries = value.Entries[min(offset, len(value.Entries)):min(offset+limit, len(value.Entries))]
		value.Sources = append(value.Sources, state.Suppressed[min(offset, len(state.Suppressed)):min(offset+limit, len(state.Suppressed))]...)
		if offset+limit < total {
			value.Next = offset + limit
		}
		return nil
	})
	return value, err
}
