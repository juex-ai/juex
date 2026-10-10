package memory

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

// Contribution is private Runtime evidence. Neither the model nor a browser can
// choose its source identity, original text or application participation epoch.
type Contribution struct {
	Epoch    int64       `json:"epoch"`
	Evidence mc.Evidence `json:"evidence"`
}
type Participation struct {
	Scope            application.Scope `json:"scope"`
	ThreadID         string            `json:"thread_id"`
	Through          uint64            `json:"through"`
	AttemptedThrough uint64            `json:"attempted_through"`
	ProcessedThrough uint64            `json:"processed_through"`
	Evidence         []mc.Evidence     `json:"evidence"`
	Manual           bool              `json:"manual"`
	AttemptedAt      time.Time         `json:"attempted_at"`
}
type ParticipationRepository interface {
	PendingParticipation(context.Context, int) ([]Participation, error)
}

func (s *Service) Maintain(ctx context.Context, frozen application.Scope, thread, reason, commandID string) (mc.Receipt, error) {
	var value mc.Receipt
	err := s.transact(ctx, frozen.Access, true, func(state *State, scope application.Scope) (err error) {
		if !scope.SameAuthority(frozen) || scope.AgentID == "" {
			return application.ErrDenied
		}
		if thread == "" || strings.TrimSpace(reason) == "" || len(reason) > 512 {
			return application.ErrInvalid
		}
		value, err = state.command(scope, commandID, struct{ Kind, Thread, Reason string }{"maintain", thread, reason}, func() (mc.Receipt, error) {
			if state.Strategy != mc.Advanced {
				return mc.Receipt{}, application.ErrDenied
			}
			key := scope.AgentID + "/" + thread
			if state.ParticipationExcluded[key] {
				return mc.Receipt{}, application.ErrDenied
			}
			p := state.Participation[key]
			if p == nil || !p.Scope.SameAuthority(scope) {
				p = &Participation{Scope: scope, ThreadID: thread}
				state.Participation[key] = p
			}
			p.Manual = true
			return mc.Receipt{ID: uuid.NewString(), State: "pending", Reason: "Maintenance requested; waits for eligible original evidence and 60 seconds idle", UpdatedAt: time.Now()}, nil
		})
		return
	})
	return value, err
}

func (s *State) Contribute(scope application.Scope, batch Contribution, now time.Time) error {
	if !s.Control.Enabled || s.Strategy != mc.Advanced || batch.Epoch != s.Control.Epoch {
		return application.ErrDisabled
	}
	e := batch.Evidence
	if !scope.Valid() || scope.AgentID == "" || e.Kind != "user" || strings.TrimSpace(e.Text) == "" || e.RecordedAt.IsZero() || e.RecordedAt.After(now.Add(time.Minute)) {
		return application.ErrInvalid
	}
	if err := mc.ValidateSource(e.Source, scope.FleetID); err != nil {
		return invalid(err.Error())
	}
	if e.Source.AgentID != scope.AgentID || e.Source.From != e.Source.Through {
		return application.ErrDenied
	}
	// Enabling starts a new boundary even if Runtime had a disconnected outbox.
	if e.RecordedAt.Before(s.AdvancedSince) || intersects(e.Source, s.Suppressed) {
		return nil
	}
	if len(e.Text) > mc.MaxBatchBytes {
		return invalid("original evidence exceeds Memory budget")
	}
	key := scope.AgentID + "/" + e.Source.ThreadID
	if s.ParticipationExcluded[key] {
		return nil
	}
	p := s.Participation[key]
	if p == nil || !p.Scope.SameAuthority(scope) {
		p = &Participation{Scope: scope, ThreadID: e.Source.ThreadID}
		s.Participation[key] = p
	}
	if e.Source.Through <= p.Through {
		return nil
	}
	bytes := len(e.Text)
	for _, prior := range p.Evidence {
		bytes += len(prior.Text)
	}
	if len(p.Evidence) >= mc.MaxBatchEvents || bytes > mc.MaxBatchBytes {
		p.Manual = true
		return application.ErrConflict
	}
	p.Evidence = append(p.Evidence, e)
	p.Through = e.Source.Through
	return nil
}

func (s *Service) Contribute(ctx context.Context, frozen application.Scope, batch Contribution) error {
	var offered error
	err := s.transact(ctx, frozen.Access, true, func(state *State, scope application.Scope) error {
		if !scope.SameAuthority(frozen) {
			return application.ErrDenied
		}
		offered = state.Contribute(scope, batch, time.Now())
		// Persist the drain request, but leave Runtime's unaccepted head in its
		// outbox. This avoids a full sub-five-Turn buffer waiting for 24 hours.
		if errors.Is(offered, application.ErrConflict) {
			return nil
		}
		return offered
	})
	if err != nil {
		return err
	}
	return offered
}

func (s *State) Advance(key string, now time.Time) (mc.Receipt, error) {
	p := s.Participation[key]
	if p == nil || s.ParticipationExcluded[key] || !s.Control.Enabled || s.Strategy != mc.Advanced || len(p.Evidence) == 0 {
		return mc.Receipt{}, nil
	}
	p.AttemptedAt = now
	if !p.Manual && (p.AttemptedThrough >= p.Through || len(p.Evidence) < 5 && now.Sub(p.Evidence[0].RecordedAt) < 24*time.Hour) {
		return mc.Receipt{}, nil
	}
	// Explicit proposals take priority. Runtime serializes automatic admission
	// per Fleet; a busy source must not block another idle source here.
	for _, review := range s.Reviews {
		if !review.WorkerFinished && (!review.Automatic || review.Scope.AgentID == p.Scope.AgentID && review.ThreadID == p.ThreadID) {
			return mc.Receipt{}, nil
		}
	}
	proposal := mc.Proposal{Key: fmt.Sprintf("advanced-%d-%d-%d", s.Control.Epoch, s.Fence, p.Through), Text: "Review the bounded original user evidence for durable shared knowledge. Reject transient instructions and do not infer unsupported facts.", Reason: "Advanced Memory maintenance", Evidence: slices.Clone(p.Evidence)}
	for _, evidence := range p.Evidence {
		proposal.Sources = append(proposal.Sources, evidence.Source)
	}
	receipt, err := s.Propose(p.Scope, p.ThreadID, proposal, true, now)
	if err != nil {
		return receipt, err
	}
	s.Reviews[receipt.ID].SourceThrough = p.Through
	p.AttemptedThrough, p.Manual = p.Through, false
	return receipt, nil
}

func (s *Service) advance(ctx context.Context) error {
	repo, ok := s.Repository.(ParticipationRepository)
	if !ok {
		return nil
	}
	items, err := repo.PendingParticipation(ctx, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		fresh, err := s.Authority.AuthorizeApplication(ctx, item.Scope.Access, true)
		if err != nil && !errors.Is(err, application.ErrDenied) {
			return err
		}
		allowed := err == nil && fresh.SameAuthority(item.Scope) && fresh.Capabilities.Allows(agentpolicy.Memory)
		if err := s.Repository.Update(ctx, item.Scope, func(state *State) error {
			key := item.Scope.AgentID + "/" + item.ThreadID
			current := state.Participation[key]
			if current == nil || !current.Scope.SameAuthority(item.Scope) {
				return nil
			}
			if !allowed {
				// This buffer is queued work, not committed knowledge. Retire it
				// so re-enabling cannot repackage evidence from revoked authority.
				current.Evidence, current.Manual = nil, false
				current.AttemptedThrough, current.AttemptedAt = current.Through, time.Now()
				return nil
			}
			_, err := state.Advance(key, time.Now())
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) settleParticipation(review *Review) {
	if !review.Automatic || !terminal(review.Receipt.State) {
		return
	}
	p := s.Participation[review.Scope.AgentID+"/"+review.ThreadID]
	if p != nil {
		if review.Receipt.State == "applied" || review.Receipt.State == "no_change" {
			p.ProcessedThrough = max(p.ProcessedThrough, review.SourceThrough)
		}
		// Failed evidence remains in its terminal Review for explicit inspection;
		// later input must not silently repackage it into a new automatic attempt.
		p.Evidence = slices.DeleteFunc(p.Evidence, func(e mc.Evidence) bool { return e.Source.Through <= review.SourceThrough })
	}
}
