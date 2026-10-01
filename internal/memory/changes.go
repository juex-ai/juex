package memory

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/memory/knowledge"
)

func invalid(reason string) error { return &application.ValidationError{Reason: reason} }

func (s *State) validateEntry(fleet string, e mc.Entry, allowed []mc.Source, human bool) error {
	if err := mc.ValidateEntryID(e.ID); err != nil {
		return invalid(err.Error())
	}
	if strings.TrimSpace(e.Name) == "" || len(e.Name) > 128 || strings.ContainsAny(e.Name, "\r\n") || strings.TrimSpace(e.Summary) == "" || len(e.Summary) > 512 || strings.ContainsAny(e.Summary, "\r\n") || len(e.Body) > mc.MaxBatchBytes || len(e.Sources) > 100 || len(e.Entities) > 50 || len(e.Facts) > 100 || len(e.Scope.Project)+len(e.Scope.Workspace) > 1024 {
		return invalid("entry exceeds content budget")
	}
	if e.Type != "user" && e.Type != "feedback" && e.Type != "project" && e.Type != "reference" {
		return invalid("unknown entry type")
	}
	if s.Deleted[e.ID] {
		return invalid("entry suppressed; explicit relearning required")
	}
	if !human && len(e.Sources) == 0 {
		return invalid("review requires source provenance")
	}
	for _, ref := range e.Sources {
		if err := s.validateSource(ref, fleet); err != nil {
			return err
		}
		if !human && !covered(ref, allowed) {
			return invalid("entry source outside assignment")
		}
	}
	entities := map[string]bool{}
	for _, entity := range e.Entities {
		if entity.ID == "" || entity.Name == "" || entity.Kind == "" || len(entity.Name) > 256 || entities[entity.ID] {
			return invalid("entities require distinct explicit identities")
		}
		entities[entity.ID] = true
	}
	for _, f := range e.Facts {
		if !entities[f.Subject] || f.Predicate == "" || ((f.Value == "") == (f.Object == "")) || (f.Object != "" && !entities[f.Object]) || f.RecordedAt.IsZero() || len(f.Sources) == 0 || len(f.Sources) > 100 {
			return invalid("fact requires entity, value, source and time")
		}
		if !slices.Contains([]string{"valid", "superseded", "disputed", "corrected", "retracted"}, f.Status) {
			return invalid("unknown temporal fact status")
		}
		if f.ValidFrom != nil && f.ValidUntil != nil && !f.ValidFrom.Before(*f.ValidUntil) {
			return invalid("invalid fact validity interval")
		}
		for _, ref := range f.Sources {
			if err := s.validateSource(ref, fleet); err != nil {
				return err
			}
			if !covered(ref, e.Sources) {
				return invalid("fact source missing from entry provenance")
			}
		}
	}
	return nil
}

func (s *State) changes(fleet string, changes []mc.Change, review *Review, human bool, now time.Time) (map[string]mc.Entry, []string, error) {
	if len(changes) == 0 || len(changes) > mc.MaxChanges {
		return nil, nil, invalid("change set requires 1–20 entries")
	}
	allowed := []mc.Source{}
	if review != nil {
		allowed = append(allowed, review.Proposal.Sources...)
	}
	for _, e := range s.Entries {
		allowed = append(allowed, e.Sources...)
	}
	candidate := maps.Clone(s.Entries)
	seen := map[string]bool{}
	ids := []string{}
	budget := 0
	for _, ch := range changes {
		e := ch.Entry
		if err := mc.ValidateEntryID(e.ID); err != nil {
			return nil, nil, invalid(err.Error())
		}
		if seen[strings.ToLower(e.ID)] {
			return nil, nil, invalid("duplicate or case-conflicting entry identity")
		}
		seen[strings.ToLower(e.ID)] = true
		for id := range candidate {
			if id != e.ID && strings.EqualFold(id, e.ID) {
				return nil, nil, invalid("case-conflicting entry identity")
			}
		}
		old, exists := s.Entries[e.ID]
		if old.Revision != ch.ExpectedRevision {
			return nil, nil, application.ErrConflict
		}
		if ch.Delete {
			if !exists {
				return nil, nil, application.ErrConflict
			}
			delete(candidate, e.ID)
			ids = append(ids, e.ID)
			continue
		}
		if err := s.validateEntry(fleet, e, allowed, human); err != nil {
			return nil, nil, err
		}
		budget += len(e.Body)
		if budget > 64<<10 {
			return nil, nil, invalid("change set exceeds content budget")
		}
		e.Revision, e.CreatedAt, e.UpdatedAt = old.Revision+1, old.CreatedAt, now
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		candidate[e.ID] = e
		ids = append(ids, e.ID)
	}
	if err := knowledge.Validate(candidate); err != nil {
		return nil, nil, invalid(err.Error())
	}
	if err := preserveFacts(s.Entries, candidate, review, human); err != nil {
		return nil, nil, err
	}
	return candidate, ids, nil
}

type ownedFact struct {
	Fact  mc.Fact
	Scope mc.Scope
}

func allFacts(entries map[string]mc.Entry) map[string]ownedFact {
	facts := map[string]ownedFact{}
	for _, entry := range entries {
		for _, fact := range entry.Facts {
			facts[fact.ID] = ownedFact{fact, entry.Scope}
		}
	}
	return facts
}

func preserveFacts(beforeEntries, afterEntries map[string]mc.Entry, review *Review, human bool) error {
	before, after := allFacts(beforeEntries), allFacts(afterEntries)
	for id, current := range after {
		old, exists := before[id]
		if exists {
			if !knowledge.SameIdentity(old.Fact, current.Fact) || old.Scope != current.Scope || !slices.Equal(old.Fact.Replaces, current.Fact.Replaces) {
				return invalid("fact identity/effective start cannot be rewritten; add a replacement")
			}
			ending := (old.Fact.Status == "valid" || old.Fact.Status == "disputed") && current.Fact.Status == "superseded"
			if !reflect.DeepEqual(old.Fact.ValidUntil, current.Fact.ValidUntil) && !ending {
				return invalid("confirmation cannot refresh validity")
			}
			if old.Fact.Status != current.Fact.Status && old.Fact.Status != "valid" && old.Fact.Status != "disputed" {
				return invalid("terminal fact cannot be revived")
			}
			for _, source := range old.Fact.Sources {
				if !covered(source, current.Fact.Sources) {
					return invalid("fact must retain original provenance")
				}
			}
		} else {
			for _, ref := range current.Fact.Replaces {
				if _, ok := after[ref]; !ok {
					return invalid("replacement target unavailable")
				}
			}
		}
		if !human && (!exists || !reflect.DeepEqual(old.Fact, current.Fact)) {
			supported := false
			if review != nil {
				for _, e := range review.Proposal.Evidence {
					if e.Kind == "user" && covered(e.Source, current.Fact.Sources) {
						supported = true
					}
				}
			}
			if !supported {
				return invalid("fact change requires current assignment original user evidence")
			}
		}
	}
	if !human {
		for id := range before {
			if _, ok := after[id]; !ok {
				return invalid("review cannot erase facts; retain semantic status or consolidate")
			}
		}
	}
	return nil
}
