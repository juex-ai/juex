package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

func terminal(status string) bool {
	return status == "applied" || status == "no_change" || status == "rejected" || status == "failed"
}
func sourceKey(agent, thread string) string { return agent + "/" + thread }
func overlaps(a, b mc.Source) bool {
	return a.FleetID == b.FleetID && a.AgentID == b.AgentID && a.ThreadID == b.ThreadID && a.From <= b.Through && b.From <= a.Through
}
func covered(ref mc.Source, allowed []mc.Source) bool {
	for _, a := range allowed {
		if overlaps(ref, a) && (a.GenerationID == "" || ref.GenerationID == a.GenerationID) && ref.From >= a.From && ref.Through <= a.Through {
			return true
		}
	}
	return false
}
func (s *Store) suppressed(ref mc.Source) bool {
	for _, a := range s.state.Suppressed {
		if overlaps(ref, a) {
			return true
		}
	}
	return false
}
func (s *Store) validateSource(ref mc.Source) error {
	if ref.FleetID != s.fleet || ref.AgentID == "" || ref.ThreadID == "" || ref.GenerationID == "" || ref.From == 0 || ref.Through < ref.From {
		return errors.New("invalid memory source range")
	}
	if s.suppressed(ref) {
		return errors.New("memory source is suppressed")
	}
	return nil
}

func (s *Store) Propose(ctx context.Context, c mc.Caller, p mc.Proposal) (mc.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Receipt{}, err
	}
	if c.Purpose == "maintenance" {
		return mc.Receipt{}, errors.New("maintenance cannot recursively propose work")
	}
	if p.Key == "" || len(p.Key) > 128 || strings.TrimSpace(p.Text) == "" || len(p.Text)+len(p.Reason) > mc.MaxBatchBytes || len(p.Sources) == 0 || len(p.Sources) > 100 || len(p.Evidence) > mc.MaxBatchEvents {
		return mc.Receipt{}, errors.New("invalid memory proposal budget")
	}
	for _, ref := range p.Sources {
		if err := s.validateSource(ref); err != nil {
			return mc.Receipt{}, err
		}
		if ref.AgentID != c.AgentID || ref.ThreadID != c.ThreadID {
			return mc.Receipt{}, errors.New("proposal source outside caller Thread")
		}
	}
	bytes := 0
	for _, e := range p.Evidence {
		bytes += len(e.Text)
		if !covered(e.Source, p.Sources) || !directEvidence(e.Kind) {
			return mc.Receipt{}, errors.New("invalid proposal evidence")
		}
	}
	if bytes > mc.MaxBatchBytes {
		return mc.Receipt{}, errors.New("proposal evidence exceeds budget")
	}
	key := "proposal/" + sourceKey(c.AgentID, c.ThreadID) + "/" + p.Key
	hash := digest(p)
	if id, ok := s.state.Keys[key]; ok {
		w := s.state.Requests[id]
		if w.Fingerprint != hash {
			return mc.Receipt{}, errors.New("idempotency key reused with different content")
		}
		return s.receipt(w), nil
	}
	before := clone(s.state)
	w := s.newWork(c, p, false)
	s.state.Keys[key] = w.Receipt.ID
	if err := s.persist(); err != nil {
		s.state = before
		return mc.Receipt{}, err
	}
	return s.receipt(w), nil
}

func (s *Store) newWork(c mc.Caller, p mc.Proposal, automatic bool) *work {
	now := s.now()
	w := &work{Receipt: mc.Receipt{ID: serviceendpoint.NewID(), State: "pending", Reason: "waiting for Supervisor", UpdatedAt: now}, Proposal: clone(p), Caller: c, Fingerprint: digest(p), Fence: s.state.Fence, Automatic: automatic}
	s.state.Requests[w.Receipt.ID] = w
	return w
}
func (s *Store) receipt(w *work) mc.Receipt {
	r := clone(w.Receipt)
	if r.Committed {
		r.IndexReady = s.indexReady
	}
	return r
}
func (s *Store) settle(w *work, status, reason string) {
	w.Receipt.State = status
	w.Receipt.Reason = reason
	w.Receipt.UpdatedAt = s.now()
}
func (s *Store) Result(ctx context.Context, c mc.Caller, id string) (mc.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Receipt{}, err
	}
	w := s.state.Requests[id]
	if w == nil || (c.Profile == mc.ProfileAgent && (w.Caller.AgentID != c.AgentID || w.Caller.ThreadID != c.ThreadID)) {
		return mc.Receipt{}, errors.New("memory request unavailable in caller scope")
	}
	return s.receipt(w), nil
}

func (s *Store) Claim(ctx context.Context, c mc.Caller) (*mc.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return nil, err
	}
	if c.Profile != mc.ProfileSupervisor || c.AssignmentID != "" {
		return nil, errors.New("claim requires Supervisor coordinator")
	}
	before := clone(s.state)
	now := s.now()
	var candidates []*work
	for _, w := range s.state.Requests {
		if w.Receipt.State == "running" {
			if now.Before(w.Expires) {
				return nil, nil
			}
			s.retry(w, "assignment lease expired")
		}
	}
	if s.state.Strategy == mc.Advanced {
		s.schedule(false, "")
	}
	for _, w := range s.state.Requests {
		if w.Receipt.State == "pending" && !now.Before(w.RetryAt) {
			if w.Automatic {
				src := s.state.Sources[w.SourceKey]
				if src == nil || !src.Enabled || !now.Before(src.LiveUntil) || src.Pending != 0 || src.IdleSince.IsZero() || now.Sub(src.IdleSince) < 60*time.Second {
					continue
				}
			}
			candidates = append(candidates, w)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Automatic != b.Automatic {
			return !a.Automatic
		}
		if !a.Receipt.UpdatedAt.Equal(b.Receipt.UpdatedAt) {
			return a.Receipt.UpdatedAt.Before(b.Receipt.UpdatedAt)
		}
		return a.Receipt.ID < b.Receipt.ID
	})
	var assignment *mc.Assignment
	if len(candidates) > 0 {
		w := candidates[0]
		w.Receipt.Attempts++
		w.Executor = c.AgentID
		w.Token = serviceendpoint.NewID()
		w.Expires = now.Add(210 * time.Second)
		w.Fence = s.state.Fence
		s.settle(w, "running", "Supervisor reviewing")
		assignment = &mc.Assignment{ID: w.Receipt.ID, Token: w.Token, ExpiresAt: w.Expires, Proposal: clone(w.Proposal), Scope: w.Caller.Scope, Automatic: w.Automatic}
	}
	if err := s.persist(); err != nil {
		s.state = before
		return nil, err
	}
	return assignment, nil
}
func (s *Store) assignment(c mc.Caller) (*work, error) {
	w := s.state.Requests[c.AssignmentID]
	if c.Profile != mc.ProfileSupervisor || w == nil || w.Executor != c.AgentID || c.Token == "" || w.Token != c.Token {
		return nil, errors.New("memory assignment capability rejected")
	}
	if w.Receipt.State != "running" || !s.now().Before(w.Expires) || w.Fence != s.state.Fence || (w.Automatic && s.state.Strategy != mc.Advanced) {
		return nil, errors.New("memory assignment expired, settled or fenced")
	}
	if w.Automatic {
		src := s.state.Sources[w.SourceKey]
		if src == nil || !src.Enabled || src.Job != w.Receipt.ID {
			return nil, errors.New("source participation lease unavailable")
		}
	}
	return w, nil
}
func (s *Store) retry(w *work, reason string) {
	w.Token = ""
	w.Executor = ""
	if w.Receipt.Attempts >= 3 {
		s.settle(w, "failed", reason)
		s.releaseSource(w)
	} else {
		s.settle(w, "pending", reason)
		w.RetryAt = s.now().Add(time.Duration(w.Receipt.Attempts*5) * time.Second)
	}
}
func (s *Store) Fail(ctx context.Context, c mc.Caller, f mc.Failure) (mc.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Receipt{}, err
	}
	w, err := s.assignment(c)
	if err != nil {
		return mc.Receipt{}, err
	}
	before := clone(s.state)
	s.retry(w, f.Reason)
	if err := s.persist(); err != nil {
		s.state = before
		return mc.Receipt{}, err
	}
	return s.receipt(w), nil
}
func (s *Store) Revoke(ctx context.Context, c mc.Caller, executor string) (mc.Settlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Settlement{}, err
	}
	if c.Profile != mc.ProfileUser && (c.Profile != mc.ProfileSupervisor || executor != c.AgentID || c.AssignmentID != "") {
		return mc.Settlement{}, errors.New("executor settlement denied")
	}
	before := clone(s.state)
	r := mc.Settlement{Confirmed: true}
	for _, w := range s.state.Requests {
		if w.Receipt.State == "running" && w.Executor == executor {
			s.retry(w, "executor stopped or replaced")
			r.Released++
		}
	}
	if err := s.persist(); err != nil {
		s.state = before
		return mc.Settlement{}, err
	}
	return r, nil
}

func (s *Store) validateEntry(e mc.Entry, allowed []mc.Source, user bool) error {
	if err := serviceendpoint.ValidateID(e.ID); err != nil {
		return err
	}
	if strings.TrimSpace(e.Name) == "" || len(e.Name) > 128 || strings.ContainsAny(e.Name, "\r\n") || strings.TrimSpace(e.Summary) == "" || len(e.Summary) > 512 || strings.ContainsAny(e.Summary, "\r\n") || len(e.Body) > mc.MaxBatchBytes || len(e.Sources) > 100 || len(e.Entities) > 50 || len(e.Facts) > 100 {
		return errors.New("invalid memory entry budget")
	}
	switch e.Type {
	case "user", "feedback", "project", "reference":
	default:
		return errors.New("invalid memory entry type")
	}
	if s.state.Deleted[e.ID] {
		return errors.New("entry suppressed; explicit user relearning required")
	}
	if !user && len(e.Sources) == 0 {
		return errors.New("reviewed knowledge requires source provenance")
	}
	for _, ref := range e.Sources {
		if err := s.validateSource(ref); err != nil {
			return err
		}
		if !user && !covered(ref, allowed) {
			return errors.New("entry source outside assignment")
		}
	}
	entities := map[string]bool{}
	for _, entity := range e.Entities {
		if entity.ID == "" || entity.Name == "" || entity.Kind == "" || entities[entity.ID] {
			return errors.New("entities require distinct explicit identities")
		}
		entities[entity.ID] = true
	}
	for _, f := range e.Facts {
		if !entities[f.Subject] || f.Predicate == "" || ((f.Value == "") == (f.Object == "")) || (f.Object != "" && !entities[f.Object]) || f.RecordedAt.IsZero() || len(f.Sources) == 0 {
			return errors.New("fact requires entity, value/relation, source and time")
		}
		if f.Status != "valid" && f.Status != "superseded" && f.Status != "disputed" {
			return errors.New("invalid temporal fact status")
		}
		if f.SourceType != "user_statement" && f.SourceType != "self_report" && f.SourceType != "observation" && f.SourceType != "derived" {
			return errors.New("invalid fact source type")
		}
		predicate := strings.ToLower(f.Predicate)
		if strings.Contains(predicate, "mbti") && f.SourceType != "self_report" {
			return errors.New("mBTI must be dated self-report")
		}
		if strings.Contains(predicate, "zodiac") && f.SourceType != "derived" {
			return errors.New("birthday-derived labels must be marked derived")
		}
		if f.ValidFrom != nil && f.ValidUntil != nil && !f.ValidFrom.Before(*f.ValidUntil) {
			return errors.New("invalid fact validity interval")
		}
		for _, ref := range f.Sources {
			if err := s.validateSource(ref); err != nil {
				return err
			}
			if !covered(ref, e.Sources) {
				return errors.New("fact source missing from entry provenance")
			}
		}
	}
	return nil
}

func (s *Store) changes(c mc.Caller, changes []mc.Change, allowed []mc.Source) ([]mc.Entry, []string, error) {
	if len(changes) == 0 || len(changes) > mc.MaxChanges {
		return nil, nil, errors.New("change set must contain 1-20 entries")
	}
	var entries []mc.Entry
	// A consolidation may retain provenance already committed in this scope.
	allowed = append([]mc.Source(nil), allowed...)
	for _, current := range s.entries {
		if visible(current.Scope, s.effectiveScope(c)) {
			allowed = append(allowed, current.Sources...)
		}
	}
	var deletes []string
	seen := map[string]bool{}
	budget := 0
	for _, ch := range changes {
		e := clone(ch.Entry)
		if err := serviceendpoint.ValidateID(e.ID); err != nil {
			return nil, nil, err
		}
		if seen[e.ID] {
			return nil, nil, errors.New("duplicate entry in change set")
		}
		seen[e.ID] = true
		for id := range s.entries {
			if id != e.ID && strings.EqualFold(id, e.ID) {
				return nil, nil, errors.New("memory identity conflicts with a different case spelling")
			}
		}
		for id := range seen {
			if id != e.ID && strings.EqualFold(id, e.ID) {
				return nil, nil, errors.New("memory change set contains case-conflicting identities")
			}
		}
		old, exists := s.entries[e.ID]
		if old.Revision != ch.ExpectedRevision {
			return nil, nil, fmt.Errorf("memory revision conflict: %s", e.ID)
		}
		if c.Profile != mc.ProfileUser {
			// Read visibility includes broader knowledge; an assignment may only
			// change knowledge in its own scope, including when deleting by ID.
			scope := s.effectiveScope(c)
			if (exists && old.Scope != scope) || (!ch.Delete && e.Scope != scope) {
				return nil, nil, errors.New("change outside assignment scope")
			}
		}
		if ch.Delete {
			if !exists {
				return nil, nil, errors.New("delete requires current entry")
			}
			deletes = append(deletes, e.ID)
			continue
		}
		if err := s.validateEntry(e, allowed, c.Profile == mc.ProfileUser); err != nil {
			return nil, nil, err
		}
		budget += len(e.Body)
		if budget > 64*1024 {
			return nil, nil, errors.New("change set exceeds content budget")
		}
		e.Revision = old.Revision + 1
		e.CreatedAt = old.CreatedAt
		if e.CreatedAt.IsZero() {
			e.CreatedAt = s.now()
		}
		e.UpdatedAt = s.now()
		entries = append(entries, e)
	}
	return entries, deletes, nil
}

func (s *Store) Decide(ctx context.Context, c mc.Caller, d mc.Decision) (mc.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Receipt{}, err
	}
	hash := digest(d)
	if w := s.state.Requests[c.AssignmentID]; w != nil && c.Profile == mc.ProfileSupervisor && w.Executor == c.AgentID && w.Token == c.Token && terminal(w.Receipt.State) && w.DecisionHash == hash {
		return s.receipt(w), nil
	}
	w, err := s.assignment(c)
	if err != nil {
		return mc.Receipt{}, err
	}
	if d.Outcome != "applied" && d.Outcome != "rejected" && d.Outcome != "no_change" {
		return mc.Receipt{}, errors.New("invalid decision outcome")
	}
	if len(d.Reason) > 4096 {
		return mc.Receipt{}, errors.New("decision reason exceeds budget")
	}
	var entries []mc.Entry
	var deletes []string
	if d.Outcome == "applied" {
		entries, deletes, err = s.changes(c, d.Changes, w.Proposal.Sources)
		if err != nil {
			return mc.Receipt{}, err
		}
	} else if len(d.Changes) > 0 {
		return mc.Receipt{}, errors.New("non-applied decision contains changes")
	}
	before := clone(s.state)
	s.settle(w, d.Outcome, d.Reason)
	w.DecisionHash = hash
	w.Receipt.Committed = d.Outcome == "applied"
	for _, e := range entries {
		w.Receipt.EntryIDs = append(w.Receipt.EntryIDs, e.ID)
		if _, ok := s.entries[e.ID]; !ok {
			s.state.Clock++
			s.state.Access[e.ID] = s.state.Clock
		}
	}
	for _, id := range deletes {
		// Model consolidation removes an entry without turning its retained
		// provenance into a user forget constraint.
		delete(s.state.Access, id)
		delete(s.state.Uses, id)
	}
	if w.Automatic && (d.Outcome == "applied" || d.Outcome == "no_change") {
		s.advanceSource(w)
	} else if w.Automatic {
		s.releaseSource(w)
	}
	if err := s.commit(before, entries, deletes); err != nil {
		return mc.Receipt{}, err
	}
	_ = s.rebuild()
	return s.receipt(s.state.Requests[c.AssignmentID]), nil
}

func (s *Store) suppressEntry(id string) {
	e := s.entries[id]
	s.state.Deleted[id] = true
	delete(s.state.Access, id)
	delete(s.state.Uses, id)
	s.state.Suppressed = append(s.state.Suppressed, e.Sources...)
	s.scrub(e.Sources)
}
func (s *Store) scrub(sources []mc.Source) {
	for _, w := range s.state.Requests {
		hit := false
		for _, ref := range w.Proposal.Sources {
			if coveredOrOverlaps(ref, sources) {
				hit = true
				break
			}
		}
		if hit {
			w.Proposal.Text = ""
			w.Proposal.Reason = ""
			w.Proposal.Evidence = nil
			w.Proposal.Sources = nil
			w.Receipt.Reason = "source suppressed"
			if !terminal(w.Receipt.State) {
				s.settle(w, "rejected", "source suppressed")
				w.Token = ""
				s.releaseSource(w)
			}
		}
	}
	for _, src := range s.state.Sources {
		filtered := src.Evidence[:0]
		for _, e := range src.Evidence {
			if !coveredOrOverlaps(e.Source, sources) {
				filtered = append(filtered, e)
			}
		}
		src.Evidence = filtered
	}
}
func coveredOrOverlaps(ref mc.Source, sources []mc.Source) bool {
	for _, source := range sources {
		if overlaps(ref, source) {
			return true
		}
	}
	return false
}

func (s *Store) Admin(ctx context.Context, c mc.Caller, q mc.AdminRequest) (mc.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.Receipt{}, err
	}
	if c.Profile != mc.ProfileUser {
		return mc.Receipt{}, errors.New("explicit user administration required")
	}
	if q.Key == "" || len(q.Key) > 128 {
		return mc.Receipt{}, errors.New("admin idempotency key required")
	}
	key := "admin/" + q.Key
	hash := digest(q)
	if id := s.state.Keys[key]; id != "" {
		w := s.state.Requests[id]
		if w.Fingerprint != hash {
			return mc.Receipt{}, errors.New("admin idempotency conflict")
		}
		return s.receipt(w), nil
	}
	before := clone(s.state)
	var entries []mc.Entry
	var deletes []string
	var err error
	switch q.Action {
	case "correct":
		entries, deletes, err = s.changes(c, q.Changes, nil)
	case "delete":
		if len(q.EntryIDs) == 0 || len(q.EntryIDs) > mc.MaxChanges {
			err = errors.New("delete requires bounded entry IDs")
			break
		}
		for _, id := range q.EntryIDs {
			if e := serviceendpoint.ValidateID(id); e != nil {
				err = e
				break
			}
			deletes = append(deletes, id)
		}
	case "no_store":
		if len(q.Sources) == 0 || len(q.Sources) > 100 {
			err = errors.New("no_store requires bounded source ranges")
			break
		}
		for _, ref := range q.Sources {
			if ref.FleetID != s.fleet || ref.AgentID == "" || ref.ThreadID == "" || ref.From == 0 || ref.Through < ref.From {
				err = errors.New("invalid no_store range")
				break
			}
		}
		for id, entry := range s.entries {
			for _, ref := range entry.Sources {
				if coveredOrOverlaps(ref, q.Sources) {
					deletes = append(deletes, id)
					break
				}
			}
		}
	case "allow_store":
		if len(q.EntryIDs)+len(q.Sources) == 0 {
			err = errors.New("explicit relearning selection required")
			break
		}
		for _, id := range q.EntryIDs {
			delete(s.state.Deleted, id)
		}
		var keep []mc.Source
		for _, ref := range s.state.Suppressed {
			if !covered(ref, q.Sources) {
				keep = append(keep, ref)
			}
		}
		s.state.Suppressed = keep
	default:
		err = errors.New("unknown memory administration action")
	}
	if err != nil {
		s.state = before
		return mc.Receipt{}, err
	}
	s.state.Fence++
	for _, w := range s.state.Requests {
		if !terminal(w.Receipt.State) {
			s.settle(w, "rejected", "superseded by explicit user control")
			w.Token = ""
			s.releaseSource(w)
		}
	}
	for _, id := range deletes {
		s.suppressEntry(id)
	}
	if q.Action == "no_store" {
		s.state.Suppressed = append(s.state.Suppressed, q.Sources...)
		s.scrub(q.Sources)
	}
	for _, e := range entries {
		if _, exists := s.entries[e.ID]; !exists {
			s.state.Clock++
			s.state.Access[e.ID] = s.state.Clock
		}
	}
	w := s.newWork(c, mc.Proposal{Key: q.Key}, false)
	w.Fingerprint = hash
	s.settle(w, "applied", "Memory-owned change committed; original Thread history and external copies are unchanged")
	w.Receipt.Committed = true
	for _, e := range entries {
		w.Receipt.EntryIDs = append(w.Receipt.EntryIDs, e.ID)
	}
	s.state.Keys[key] = w.Receipt.ID
	id := w.Receipt.ID
	if err := s.commit(before, entries, deletes); err != nil {
		return mc.Receipt{}, err
	}
	_ = s.rebuild()
	return s.receipt(s.state.Requests[id]), nil
}
