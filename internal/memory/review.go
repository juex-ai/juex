package memory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func digest(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func overlaps(a, b mc.Source) bool {
	return a.FleetID == b.FleetID && a.AgentID == b.AgentID && a.ThreadID == b.ThreadID && a.From <= b.Through && b.From <= a.Through
}

func covered(ref mc.Source, sources []mc.Source) bool {
	for _, source := range sources {
		if overlaps(ref, source) && (source.GenerationID == "" || source.GenerationID == ref.GenerationID) && ref.From >= source.From && ref.Through <= source.Through {
			return true
		}
	}
	return false
}

func intersects(ref mc.Source, sources []mc.Source) bool {
	for _, source := range sources {
		if overlaps(ref, source) {
			return true
		}
	}
	return false
}

func (s *State) validateSource(ref mc.Source, fleet string) error {
	if err := mc.ValidateSource(ref, fleet); err != nil {
		return fmt.Errorf("%w: %v", application.ErrInvalid, err)
	}
	if intersects(ref, s.Suppressed) {
		return fmt.Errorf("%w: source is suppressed", application.ErrDenied)
	}
	return nil
}

// Propose receives original evidence injected by Runtime, not prose supplied by
// a model claiming to be a user. Acceptance is not a knowledge commit.
func (s *State) Propose(scope application.Scope, thread string, p mc.Proposal, automatic bool, now time.Time) (mc.Receipt, error) {
	if !s.Control.Enabled {
		return mc.Receipt{}, application.ErrDisabled
	}
	if !scope.Valid() || scope.AgentID == "" || thread == "" || p.Key == "" || len(p.Key) > 128 || strings.TrimSpace(p.Text) == "" || len(p.Text)+len(p.Reason) > mc.MaxBatchBytes || len(p.Sources) == 0 || len(p.Sources) > 100 || len(p.Evidence) == 0 || len(p.Evidence) > mc.MaxBatchEvents {
		return mc.Receipt{}, application.ErrInvalid
	}
	if automatic && s.Strategy != mc.Advanced {
		return mc.Receipt{}, application.ErrDenied
	}
	for _, ref := range p.Sources {
		if err := s.validateSource(ref, scope.FleetID); err != nil {
			return mc.Receipt{}, err
		}
		if ref.AgentID != scope.AgentID || ref.ThreadID != thread {
			return mc.Receipt{}, application.ErrDenied
		}
	}
	bytes := 0
	for _, evidence := range p.Evidence {
		bytes += len(evidence.Text)
		if !covered(evidence.Source, p.Sources) || evidence.Kind != "user" && evidence.Kind != "assistant" || evidence.RecordedAt.IsZero() {
			return mc.Receipt{}, application.ErrInvalid
		}
	}
	if bytes > mc.MaxBatchBytes {
		return mc.Receipt{}, application.ErrInvalid
	}
	key := scope.AgentID + "/" + thread + "/" + p.Key
	hash := digest(struct {
		Proposal  mc.Proposal
		Automatic bool
	}{p, automatic})
	if id := s.Keys[key]; id != "" {
		w := s.Reviews[id]
		if w.Fingerprint != hash || w.Scope != scope {
			return mc.Receipt{}, application.ErrConflict
		}
		return w.Receipt, nil
	}
	if s.Status().Pending >= 100 {
		return mc.Receipt{}, fmt.Errorf("%w: pending review capacity reached", application.ErrConflict)
	}
	id := uuid.NewString()
	w := &Review{ID: id, Scope: scope, ThreadID: thread, Proposal: p, Epoch: s.Control.Epoch, Fence: s.Fence, Fingerprint: hash, Automatic: automatic,
		Receipt: mc.Receipt{ID: id, State: "pending", Reason: "waiting for review Worker", UpdatedAt: now}}
	s.Reviews[id] = w
	s.Keys[key] = id
	return w.Receipt, nil
}

func (s *State) Review(scope application.Scope, binding Binding) (*Review, error) {
	w := s.Reviews[binding.ReviewID]
	if w == nil || w.Scope != scope || w.Epoch != binding.Epoch || w.Fence != binding.Fence {
		return nil, application.ErrDenied
	}
	if !s.Control.Enabled {
		return nil, application.ErrDisabled
	}
	if w.Epoch != s.Control.Epoch || w.Fence != s.Fence || terminal(w.Receipt.State) {
		return nil, application.ErrConflict
	}
	return w, nil
}

func (s *State) Decide(scope application.Scope, binding Binding, decision mc.Decision, now time.Time) (mc.Receipt, error) {
	w := s.Reviews[binding.ReviewID]
	hash := digest(decision)
	// A receipt may be read again after disable/correction; this cannot apply a
	// second mutation or resurrect knowledge removed by human administration.
	if w != nil && w.Scope == scope && w.Epoch == binding.Epoch && w.Fence == binding.Fence && terminal(w.Receipt.State) && w.DecisionHash == hash {
		return w.Receipt, nil
	}
	w, err := s.Review(scope, binding)
	if err != nil {
		return mc.Receipt{}, err
	}
	if decision.Outcome != "applied" && decision.Outcome != "rejected" && decision.Outcome != "no_change" || strings.TrimSpace(decision.Reason) == "" || len(decision.Reason) > 4096 {
		return mc.Receipt{}, application.ErrInvalid
	}
	var ids []string
	if decision.Outcome == "applied" {
		candidate, changed, err := s.changes(scope.FleetID, decision.Changes, w, false, now)
		if err != nil {
			return mc.Receipt{}, err
		}
		s.Entries, ids = candidate, changed
	} else if len(decision.Changes) != 0 {
		return mc.Receipt{}, application.ErrInvalid
	}
	w.DecisionHash = hash
	w.Receipt.State, w.Receipt.Reason = decision.Outcome, decision.Reason
	w.Receipt.Committed, w.Receipt.IndexReady = decision.Outcome == "applied", true
	w.Receipt.EntryIDs, w.Receipt.UpdatedAt = ids, now
	return w.Receipt, nil
}

func (s *State) Result(scope application.Scope, thread, id string) (mc.Receipt, error) {
	w := s.Reviews[id]
	if w == nil || w.Scope.FleetID != scope.FleetID || scope.AgentID != "" && (scope.AgentID != w.Scope.AgentID || thread != w.ThreadID) {
		return mc.Receipt{}, application.ErrDenied
	}
	return w.Receipt, nil
}

func (s *State) scrub(sources []mc.Source) {
	for _, w := range s.Reviews {
		for _, ref := range w.Proposal.Sources {
			if intersects(ref, sources) {
				w.Proposal.Text, w.Proposal.Reason = "", ""
				w.Proposal.Sources, w.Proposal.Evidence = nil, nil
				w.Receipt.Reason = "source suppressed"
				break
			}
		}
	}
}
