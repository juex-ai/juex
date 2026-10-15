package service

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/features/memory/knowledge"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func (s *Store) Domains(ctx context.Context, c mc.Caller, q mc.DomainRequest) ([]mc.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return nil, err
	}
	out, err := knowledge.Domains(q.ID)
	return clone(out), err
}
func (s *Store) Facts(ctx context.Context, c mc.Caller, q mc.Query) (mc.FactPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.begin(ctx, c); err != nil {
		return mc.FactPage{}, err
	}
	if err := knowledge.ValidateQuery(q); err != nil {
		return mc.FactPage{}, err
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	page := mc.FactPage{Facts: []mc.FactView{}, Next: -1, Fence: s.state.Fence}
	var matches []mc.FactView
	now := s.now()
	terms := strings.Fields(strings.ToLower(q.Text))
	for _, e := range s.entries {
		for _, f := range e.Facts {
			if q.Domain == "" || q.Domain == f.Domain {
				page.DomainTotal++
			}
		}
		for _, v := range knowledge.Select(e, q, now) {
			text := strings.ToLower(knowledge.Text(v) + " " + v.Fact.ID + " " + v.Fact.Domain)
			match := len(terms) == 0
			for _, t := range terms {
				if strings.Contains(text, t) {
					match = true
				}
			}
			if match {
				matches = append(matches, v)
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Fact.ID == matches[j].Fact.ID {
			return matches[i].EntryID < matches[j].EntryID
		}
		return matches[i].Fact.ID < matches[j].Fact.ID
	})
	page.Total = len(matches)
	for i := q.Offset; i < len(matches) && len(page.Facts) < q.Limit; i++ {
		page.Facts = append(page.Facts, matches[i])
	}
	if q.Offset+len(page.Facts) < len(matches) {
		page.Next = q.Offset + len(page.Facts)
	}
	return clone(page), nil
}

type factOwner struct {
	fact  mc.Fact
	scope mc.Scope
}

func allFacts(entries map[string]mc.Entry) map[string]factOwner {
	out := map[string]factOwner{}
	for _, e := range entries {
		for _, f := range e.Facts {
			out[f.ID] = factOwner{f, e.Scope}
		}
	}
	return out
}
func (s *Store) validateKnowledgeChanges(c mc.Caller, entries []mc.Entry, deletes []string) error {
	candidate := clone(s.entries)
	for _, id := range deletes {
		delete(candidate, id)
	}
	for _, e := range entries {
		candidate[e.ID] = e
	}
	if err := knowledge.Validate(candidate); err != nil {
		return err
	}
	before, after := allFacts(s.entries), allFacts(candidate)
	for id, current := range after {
		old, exists := before[id]
		if exists {
			if !knowledge.SameIdentity(old.fact, current.fact) || old.scope != current.scope || !slices.Equal(old.fact.Replaces, current.fact.Replaces) {
				return fmt.Errorf("memory fact %s identity/effective start cannot be rewritten; retain it as corrected and add a replacement", id)
			}
			endingAssertion := (old.fact.Status == "valid" || old.fact.Status == "disputed") && current.fact.Status == "superseded"
			if !reflect.DeepEqual(old.fact.ValidUntil, current.fact.ValidUntil) && !endingAssertion {
				return fmt.Errorf("memory confirmation cannot refresh validity for %s", id)
			}
			if old.fact.Status != current.fact.Status && old.fact.Status != "valid" && old.fact.Status != "disputed" {
				return fmt.Errorf("memory terminal fact %s cannot be revived", id)
			}
			for _, source := range old.fact.Sources {
				if !covered(source, current.fact.Sources) {
					return fmt.Errorf("memory fact %s must retain original provenance", id)
				}
			}
		}
		if !exists {
			for _, ref := range current.fact.Replaces {
				if _, ok := after[ref]; !ok {
					return fmt.Errorf("memory replacement target %s unavailable", ref)
				}
			}
		}
		changed := !exists || !reflect.DeepEqual(old.fact, current.fact)
		if changed && c.Profile != mc.ProfileUser {
			w := s.state.Requests[c.AssignmentID]
			supported := false
			if w != nil {
				for _, e := range w.Proposal.Evidence {
					if e.Kind == "user" && covered(e.Source, current.fact.Sources) {
						supported = true
					}
				}
			}
			if !supported {
				return fmt.Errorf("memory fact %s change requires current assignment original user evidence; old provenance or assistant repetition is not confirmation", id)
			}
		}
	}
	if c.Profile != mc.ProfileUser {
		for id := range before {
			if _, ok := after[id]; !ok {
				return fmt.Errorf("memory fact %s cannot be erased by maintenance; retain it with semantic status or move it during consolidation", id)
			}
		}
	}
	return nil
}
