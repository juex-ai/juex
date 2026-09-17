package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func directEvidence(kind string) bool { return kind == "user" || kind == "assistant" }
func (s *Store) Participation(ctx context.Context, c mc.Caller, b mc.Boundary) (mc.SourceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.SourceState{}, err
	}
	if c.Purpose == "maintenance" || b.ThreadID == "" || b.Epoch == "" {
		return mc.SourceState{}, errors.New("invalid source participation")
	}
	key := sourceKey(c.AgentID, b.ThreadID)
	old := s.state.Sources[key]
	if old != nil && old.Epoch == b.Epoch {
		if old.Enabled != b.Enabled {
			return mc.SourceState{}, errors.New("participation epoch conflict")
		}
		before := clone(s.state)
		old.LiveUntil = s.now().Add(30 * time.Second)
		if err := s.persist(); err != nil {
			s.state = before
			return mc.SourceState{}, err
		}
		return old.SourceState, nil
	}
	before := clone(s.state)
	if old != nil && old.Job != "" {
		if w := s.state.Requests[old.Job]; w != nil && !terminal(w.Receipt.State) {
			s.settle(w, "rejected", "participation boundary changed")
			w.Token = ""
		}
	}
	c.ThreadID = b.ThreadID
	next := &sourceState{SourceState: mc.SourceState{Epoch: b.Epoch, Enabled: b.Enabled, AcceptedThrough: b.Cursor, ProcessedThrough: b.Cursor}, Caller: c, LiveUntil: s.now().Add(30 * time.Second)}
	s.state.Sources[key] = next
	if err := s.persist(); err != nil {
		s.state = before
		return mc.SourceState{}, err
	}
	return next.SourceState, nil
}

func (s *Store) Contribute(ctx context.Context, c mc.Caller, b mc.SourceBatch) (mc.SourceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.SourceState{}, err
	}
	src := s.state.Sources[sourceKey(c.AgentID, b.ThreadID)]
	if src == nil || !src.Enabled || src.Epoch != b.Epoch || b.Maintenance || c.Purpose == "maintenance" {
		return mc.SourceState{}, errors.New("source participation unavailable")
	}
	if b.Through < b.After || b.After != src.AcceptedThrough || len(b.Evidence) > mc.MaxBatchEvents || len(b.EndedGenerations) > 1000 {
		return src.SourceState, errors.New("source range conflict or budget exceeded")
	}
	bytes := 0
	for _, e := range src.Evidence {
		bytes += len(e.Text)
	}
	for _, e := range b.Evidence {
		bytes += len(e.Text)
		ref := e.Source
		if ref.FleetID != s.fleet || ref.AgentID != c.AgentID || ref.ThreadID != b.ThreadID || ref.From <= b.After || ref.Through > b.Through || ref.Through < ref.From || ref.GenerationID == "" || !directEvidence(e.Kind) {
			return src.SourceState, errors.New("source evidence outside frozen range")
		}
	}
	if bytes > mc.MaxBatchBytes || len(src.Evidence)+len(b.Evidence) > mc.MaxBatchEvents {
		return src.SourceState, errors.New("source buffer full; wait for maintenance or request manual maintenance")
	}
	before := clone(s.state)
	src.AcceptedThrough = b.Through
	src.IdleSince = b.IdleSince
	src.Pending = b.Pending
	src.EndedGenerations = append([]string(nil), b.EndedGenerations...)
	for _, e := range b.Evidence {
		if !s.suppressed(e.Source) {
			src.Evidence = append(src.Evidence, clone(e))
		}
	}
	if src.FirstPending.IsZero() && src.AcceptedThrough > src.ProcessedThrough {
		src.FirstPending = s.now()
	}
	if err := s.persist(); err != nil {
		s.state = before
		return mc.SourceState{}, err
	}
	return src.SourceState, nil
}

func (s *Store) schedule(manual bool, only string) *work {
	if s.state.Strategy != mc.Advanced {
		return nil
	}
	for key, src := range s.state.Sources {
		if (only != "" && key != only) || !src.Enabled || !s.now().Before(src.LiveUntil) || src.Job != "" || src.AcceptedThrough <= src.ProcessedThrough || src.Pending != 0 || src.IdleSince.IsZero() || s.now().Sub(src.IdleSince) < 60*time.Second {
			continue
		}
		if !manual && len(src.EndedGenerations) < 5 && (src.FirstPending.IsZero() || s.now().Sub(src.FirstPending) < 24*time.Hour) {
			continue
		}
		p := mc.Proposal{Key: fmt.Sprintf("maintenance/%s/%s/%d/%d", key, src.Epoch, src.ProcessedThrough, src.AcceptedThrough), Text: "Review bounded original dialogue for stable, useful knowledge. Compare existing scoped knowledge, preserve direct provenance and temporal uncertainty; commit a bounded change or no_change.", Reason: "bounded history maintenance", Evidence: clone(src.Evidence)}
		for _, e := range p.Evidence {
			p.Sources = append(p.Sources, e.Source)
		}
		w := s.newWork(src.Caller, p, true)
		w.SourceKey = key
		w.Through = src.AcceptedThrough
		src.Job = w.Receipt.ID
		return w
	}
	return nil
}

func (s *Store) Maintain(ctx context.Context, c mc.Caller, thread string) (mc.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Receipt{}, err
	}
	if c.Purpose == "maintenance" {
		return mc.Receipt{}, errors.New("maintenance recursion denied")
	}
	key := sourceKey(c.AgentID, thread)
	src := s.state.Sources[key]
	if src != nil && src.Job != "" {
		return s.receipt(s.state.Requests[src.Job]), nil
	}
	before := clone(s.state)
	w := s.schedule(true, key)
	if w == nil {
		return mc.Receipt{}, errors.New("manual maintenance requires Advanced, an idle participating Thread and unprocessed evidence")
	}
	if err := s.persist(); err != nil {
		s.state = before
		return mc.Receipt{}, err
	}
	return s.receipt(w), nil
}

func (s *Store) advanceSource(w *work) {
	if src := s.state.Sources[w.SourceKey]; src != nil && src.Job == w.Receipt.ID {
		src.ProcessedThrough = w.Through
		src.Job = ""
		var remaining []mc.Evidence
		for _, e := range src.Evidence {
			if e.Source.Through > w.Through {
				remaining = append(remaining, e)
			}
		}
		src.Evidence = remaining
		if src.AcceptedThrough == w.Through {
			src.FirstPending = time.Time{}
			src.EndedGenerations = nil
		}
	}
}
func (s *Store) releaseSource(w *work) {
	if src := s.state.Sources[w.SourceKey]; src != nil && src.Job == w.Receipt.ID {
		src.Job = ""
	}
}

func (s *Store) History(ctx context.Context, c mc.Caller, q mc.Source) ([]mc.Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return nil, err
	}
	if err := s.validateSource(q); err != nil {
		return nil, err
	}
	if q.Through-q.From > 10000 {
		return nil, errors.New("history range exceeds budget")
	}
	var candidates []mc.Evidence
	if c.Profile == mc.ProfileSupervisor && c.AssignmentID != "" {
		w, err := s.assignment(c)
		if err != nil {
			return nil, err
		}
		if !covered(q, w.Proposal.Sources) {
			return nil, errors.New("history outside assignment")
		}
		candidates = w.Proposal.Evidence
	} else {
		if q.AgentID != c.AgentID || q.ThreadID != c.ThreadID {
			return nil, errors.New("history outside caller Thread")
		}
		if src := s.state.Sources[sourceKey(q.AgentID, q.ThreadID)]; src != nil {
			candidates = src.Evidence
		}
	}
	result := []mc.Evidence{}
	for _, e := range candidates {
		if overlaps(q, e.Source) && !s.suppressed(e.Source) {
			result = append(result, clone(e))
		}
	}
	if len(result) == 0 {
		return nil, errors.New("source evidence unavailable in retained bounded snapshot")
	}
	return result, nil
}
