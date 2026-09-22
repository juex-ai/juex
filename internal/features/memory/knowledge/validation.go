package knowledge

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

type ownedFact struct {
	fact  mc.Fact
	scope mc.Scope
	entry string
}

func key(f mc.Fact, scope mc.Scope, r mc.Relation) string {
	parts := []string{f.Domain}
	for _, field := range r.Competition {
		value := ""
		switch field {
		case "subject":
			value = f.Subject
		case "predicate":
			value = f.Predicate
		case "workspace":
			value = scope.Workspace
		case "project":
			value = scope.Project
		default:
			value = f.Qualifiers[field]
		}
		parts = append(parts, field, value)
	}
	return strings.Join(parts, "\x00")
}
func overlaps(a, b mc.Fact) bool {
	return !(a.ValidUntil != nil && b.ValidFrom != nil && !b.ValidFrom.Before(*a.ValidUntil) || b.ValidUntil != nil && a.ValidFrom != nil && !a.ValidFrom.Before(*b.ValidUntil))
}

// A known start is required by as-of queries; an ended assertion also needs
// its known end. Unknown transitions cannot establish historical truth.
func knownEffectiveTruth(f mc.Fact) bool {
	return f.ValidFrom != nil && (f.Status == "valid" || f.Status == "superseded" && f.ValidUntil != nil)
}

func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
func SameIdentity(a, b mc.Fact) bool {
	return a.ID == b.ID && a.Domain == b.Domain && a.Subject == b.Subject && a.Predicate == b.Predicate && a.Value == b.Value && a.Object == b.Object && a.SourceType == b.SourceType && a.RecordedAt.Equal(b.RecordedAt) && sameTime(a.ValidFrom, b.ValidFrom) && sameTime(a.DueAt, b.DueAt) && sameMap(a.Qualifiers, b.Qualifiers)
}
func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Validate sees the complete post-transaction state, including unmodified entries.
func Validate(entries map[string]mc.Entry) error {
	entities := map[string]mc.Entity{}
	facts := map[string]ownedFact{}
	groups := map[string][]ownedFact{}
	for _, e := range entries {
		local := map[string]mc.Entity{}
		for _, entity := range e.Entities {
			if err := mc.ValidateEntryID(entity.ID); err != nil {
				return fmt.Errorf("memory entity identity: %w", err)
			}
			if old, ok := entities[entity.ID]; ok && old.Kind != entity.Kind {
				return fmt.Errorf("memory entity %s has conflicting kinds", entity.ID)
			}
			entities[entity.ID] = entity
			local[entity.ID] = entity
		}
		for _, f := range e.Facts {
			if err := mc.ValidateEntryID(f.ID); err != nil {
				return fmt.Errorf("memory fact identity: %w", err)
			}
			if _, ok := facts[f.ID]; ok {
				return fmt.Errorf("memory duplicate fact ID %s; consolidate sources into its owning entry", f.ID)
			}
			r, ok := Relation(f.Domain, f.Predicate)
			if !ok {
				return fmt.Errorf("memory unknown relation %s/%s; read domain template or use other/open_fact", f.Domain, f.Predicate)
			}
			if !slices.Contains(r.SourceTypes, f.SourceType) {
				return fmt.Errorf("memory fact %s requires source_type %v", f.ID, r.SourceTypes)
			}
			if !slices.Contains(r.Subjects, "any") && !slices.Contains(r.Subjects, local[f.Subject].Kind) {
				return fmt.Errorf("memory fact %s requires subject type %v", f.ID, r.Subjects)
			}
			if len(r.Objects) > 0 {
				if f.Object == "" || !slices.Contains(r.Objects, "any") && !slices.Contains(r.Objects, local[f.Object].Kind) {
					return fmt.Errorf("memory fact %s requires object type %v", f.ID, r.Objects)
				}
			} else if f.Object != "" || strings.TrimSpace(f.Value) == "" {
				return fmt.Errorf("memory fact %s requires %s literal", f.ID, r.ValueType)
			}
			if r.ValueType == "number" {
				v, err := strconv.ParseFloat(f.Value, 64)
				if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
					return fmt.Errorf("memory fact %s requires finite numeric value", f.ID)
				}
			}
			for _, q := range r.Qualifiers {
				if strings.TrimSpace(f.Qualifiers[q]) == "" {
					return fmt.Errorf("memory fact %s requires qualifier %s", f.ID, q)
				}
			}
			for q, v := range f.Qualifiers {
				if !slices.Contains(r.Qualifiers, q) && !slices.Contains(r.OptionalQualifiers, q) {
					return fmt.Errorf("memory fact %s has undeclared qualifier %s", f.ID, q)
				}
				if strings.TrimSpace(v) == "" || len(v) > 256 {
					return fmt.Errorf("memory invalid qualifier %s", q)
				}
			}
			if len(f.Reason) == 0 || len(f.Reason) > 2048 || len(f.TimeNote) > 512 || len(f.Value) > 4096 || len(f.Replaces) > 20 {
				return fmt.Errorf("memory fact %s requires bounded reason, value and references", f.ID)
			}
			if f.Status == "superseded" && f.ValidUntil == nil && f.TimeNote == "" {
				return fmt.Errorf("memory superseded fact %s requires supported valid_until or an explicit time_note for an unknown end", f.ID)
			}
			if f.Domain == "obligations" && f.Status == "valid" && f.ValidUntil != nil {
				return fmt.Errorf("memory obligation %s uses due_at, not valid_until, for a deadline", f.ID)
			}
			if f.Domain != "obligations" && f.DueAt != nil {
				return fmt.Errorf("memory due_at belongs to obligations")
			}
			o := ownedFact{f, e.Scope, e.ID}
			facts[f.ID] = o
			group := key(f, e.Scope, r)
			for _, other := range groups[group] {
				if !overlaps(f, other.fact) {
					continue
				}
				if r.Cardinality == "one" && knownEffectiveTruth(f) && knownEffectiveTruth(other.fact) {
					return fmt.Errorf("memory competing effective-time facts %s (%s) and %s (%s); historical single-value intervals cannot overlap", other.fact.ID, other.entry, f.ID, e.ID)
				}
				if f.Status == "valid" && other.fact.Status == "valid" {
					if r.Cardinality == "one" {
						return fmt.Errorf("memory competing facts %s (%s) and %s (%s); close supported intervals or mark unresolved claims disputed", other.fact.ID, other.entry, f.ID, e.ID)
					}
					if f.Value == other.fact.Value && f.Object == other.fact.Object {
						return fmt.Errorf("memory duplicate assertion %s and %s; confirm the existing fact with sources", other.fact.ID, f.ID)
					}
				}
				if r.Cardinality == "one" && (f.Status == "disputed" && other.fact.Status == "valid" || f.Status == "valid" && other.fact.Status == "disputed") {
					return fmt.Errorf("memory competing disputed and certain claims %s/%s; preserve both as disputed until resolved", other.fact.ID, f.ID)
				}
			}
			groups[group] = append(groups[group], o)
		}
	}
	for _, o := range facts {
		for _, id := range o.fact.Replaces {
			prior, exists := facts[id]
			if !exists {
				continue
			} // User forget may remove the referenced fact.
			if id == o.fact.ID || prior.fact.Status != "superseded" && prior.fact.Status != "corrected" {
				return fmt.Errorf("memory replacement %s must reference a superseded/corrected fact", o.fact.ID)
			}
			if prior.fact.Status == "superseded" {
				r, _ := Relation(o.fact.Domain, o.fact.Predicate)
				if key(o.fact, o.scope, r) != key(prior.fact, prior.scope, r) || !sameTime(o.fact.ValidFrom, prior.fact.ValidUntil) || (o.fact.ValidFrom == nil && (o.fact.TimeNote == "" || prior.fact.TimeNote == "")) {
					return fmt.Errorf("memory supersession %s must preserve competition scope and meet the prior interval", o.fact.ID)
				}
			}
		}
	}
	// Shared replacement ancestors must be checked once, not once per path.
	checked := map[string]bool{}
	for id := range facts {
		seen := map[string]bool{}
		var visit func(string) error
		visit = func(id string) error {
			if checked[id] {
				return nil
			}
			if seen[id] {
				return fmt.Errorf("memory replacement cycle at %s", id)
			}
			seen[id] = true
			for _, r := range facts[id].fact.Replaces {
				if err := visit(r); err != nil {
					return err
				}
			}
			delete(seen, id)
			checked[id] = true
			return nil
		}
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
